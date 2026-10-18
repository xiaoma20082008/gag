package gateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"

	"gag/internal/admin"
	"gag/internal/server"
)

// splitListen 把 "host:port" 拆成 http.Server 需要的 host 与端口。
//
// 配置里写的是一个地址字符串，而两个 server 的构造函数都要求分开的
// host/port，所以拆分与校验在这里做一次，别让错误延迟到 Listen 才暴露。
func splitListen(addr string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, fmt.Errorf("%q 不是合法的 host:port: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 0 || port > 65535 {
		return "", 0, fmt.Errorf("%q 的端口非法", addr)
	}
	return host, port, nil
}

// shutdown 并行关闭两个端口：它们互不依赖，串行关闭只会拉长停机时间。
//
// 每个端口的错误都要收集而不是遇到第一个就返回——只报一个会让
// 运维以为另一个关干净了。ctx 超时则直接返回，不再等在途请求。
func shutdown(ctx context.Context, gag *server.GagServer, adminSrv *admin.AdminServer) error {
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	collect := func(err error) {
		if err == nil {
			return
		}
		mu.Lock()
		errs = append(errs, err)
		mu.Unlock()
	}

	if gag != nil {
		wg.Add(1)
		go func() { defer wg.Done(); collect(gag.Close()) }()
	}
	if adminSrv != nil {
		wg.Add(1)
		go func() { defer wg.Done(); collect(adminSrv.Close()) }()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
		return errors.Join(errs...)
	case <-ctx.Done():
		return fmt.Errorf("停机超时: %w", ctx.Err())
	}
}

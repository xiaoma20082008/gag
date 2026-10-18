// Command main 是网关的可执行入口。
//
// 它只做三件属于"进程"的事：解析参数、把信号转成 ctx、决定退出码。
// 网关怎么装配、怎么启动、怎么停机都在 internal/gateway 里。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"gag/internal/config"
	"gag/internal/gateway"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gateway 启动失败:", err)
		os.Exit(1)
	}
}

func run() error {
	configType := flag.String("t", "file", "配置中心类型")
	configMeta := flag.String("m", "configs/gateway.yaml", "配置中心路径")
	flag.Parse()

	cfg := &config.ConfigClientConfig{

		Type: *configType,
		Meta: *configMeta,

		LogLevel: "info",

		// file
		Path:     *configMeta,
		Format:   "yaml",
		Interval: 1,

		// nacos
		Namespace: "public",
		Group:     "gag",
		DataId:    "gateway.yaml",
		Protocol:  "http",
		RootPath:  "/nacos",
		TimeoutMs: 5000,
	}

	gw, err := gateway.New(cfg)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		gw.Logger().Info("收到退出信号，开始优雅停机", map[string]any{"signal": sig.String()})
		cancel()
	}()

	return gw.Run(ctx)
}

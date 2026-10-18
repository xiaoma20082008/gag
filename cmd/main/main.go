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
	configPath := flag.String("c", "configs/gateway.yaml", "配置文件路径")
	flag.Parse()

	client, err := config.NewFileConfigClient(&config.ConfigClientConfig{
		Type: "file",
		Path: *configPath,
	})
	if err != nil {
		return err
	}

	gw, err := gateway.New(client)
	if err != nil {
		_ = client.Close()
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

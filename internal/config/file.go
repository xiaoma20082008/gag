package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// defaultFilePollInterval 是文件变更的轮询间隔。
const defaultFilePollInterval = time.Second

// FileConfigClient 从本地文件读取配置。
type FileConfigClient struct {
	path     string
	interval time.Duration

	// mu 保护 data：轮询 goroutine 与 Get 会并发访问。
	mu sync.RWMutex
	// data 记录每个文件"最近一次成功读到的内容"，用于判断是否真的变了。
	data map[string]string

	closed    chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

// NewFileConfigClient 构造文件来源。
func NewFileConfigClient(cfg *ConfigClientConfig) (*FileConfigClient, error) {
	if cfg == nil {
		return nil, errors.New("config: file 客户端配置为 nil")
	}
	// 类型写错说明装配代码接错了分支，早报比跑起来再查强。
	if cfg.Type != "" && cfg.Type != "file" {
		return nil, fmt.Errorf("config: file 客户端收到 type=%q", cfg.Type)
	}
	path := strings.TrimSpace(cfg.Path)
	if path == "" {
		return nil, errors.New("config: type=file 需要 path")
	}
	// 格式也在这里校验：扩展名不支持不该拖到第一次 Get 才暴露。
	if _, err := FormatOf(path); err != nil {
		return nil, err
	}

	interval := cfg.Interval.Std()
	if interval <= 0 {
		interval = defaultFilePollInterval
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: 读取 %s: %w", path, err)
	}

	return &FileConfigClient{
		path:     path,
		interval: interval,
		data:     map[string]string{path: string(content)},
		closed:   make(chan struct{}),
	}, nil
}

func (c *FileConfigClient) Type() string { return "file" }

// Path 返回构造时指定的默认配置路径。
func (c *FileConfigClient) Path() string { return c.path }

// Get 读取文件内容。
func (c *FileConfigClient) Get(ctx context.Context, key, def string) string {
	path := c.resolve(key)

	if err := ctx.Err(); err != nil {
		return def
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return def
	}

	c.mu.Lock()
	c.data[path] = string(content)
	c.mu.Unlock()
	return string(content)
}

// OnChange 轮询文件，内容真的变化时才回调。
//
// 只比较内容不比较 mtime：touch、编辑器保存未改动、ConfigMap 重挂
// 都会让 mtime 变，但内容没变——那时触发一次热加载纯属浪费。
func (c *FileConfigClient) OnChange(ctx context.Context, key string, f func(newVal string)) {
	if f == nil {
		return
	}
	path := c.resolve(key)

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()

		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-c.closed:
				return
			case <-ticker.C:
			}

			content, err := os.ReadFile(path)
			if err != nil {
				// 读不到（正在被替换、临时无权限）不当作变更。
				// 把空内容当成新配置推给数据面，等于用空配置覆盖掉好的那份。
				continue
			}
			if c.changed(path, string(content)) {
				f(string(content))
			}
		}
	}()
}

func (c *FileConfigClient) changed(path, content string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.data[path]; ok && old == content {
		return false
	}
	c.data[path] = content
	return true
}

func (c *FileConfigClient) resolve(key string) string {
	if key = strings.TrimSpace(key); key != "" {
		return key
	}
	return c.path
}

// Close 停止所有轮询并等待它们退出。
func (c *FileConfigClient) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	c.wg.Wait()
	return nil
}

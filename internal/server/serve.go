package server

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Start 绑定端口并开始服务，阻塞直到 Close 或监听出错。
func (s *GagServer) Start() error {
	if s.chain.Load() == nil {
		return errors.New("server: 尚未应用任何配置，拒绝启动")
	}

	ln, err := s.listen()
	if err != nil {
		return err
	}

	if s.Logger != nil {
		st := s.state.Load()
		s.Logger.Info("业务端口开始监听", map[string]any{
			"addr":    ln.Addr().String(),
			"version": st.Snapshot().Version(),
			"hash":    st.Snapshot().Hash()[:12],
			"routes":  st.RouteCount(),
		})
	}

	// Serve 在原生监听器上循环 accept net.Conn；HTTP 解析、keep-alive、
	// 连接超时都由标准库处理，我们只在 handler 里执行治理链。
	if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// 显式持有 net.Listener 而不是用 ListenAndServe，是为了让绑定错误
// 在 Start 返回前就暴露（端口被占用、权限不足），而不是异步丢进日志。
func (s *GagServer) listen() (net.Listener, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln != nil {
		return s.ln, nil
	}
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return nil, fmt.Errorf("server: 监听 %s 失败: %w", s.addr, err)
	}
	s.ln = &keepAliveListener{Listener: ln}
	return s.ln, nil
}

// keepAliveListener 在 accept 时给连接打开 TCP keepalive。
type keepAliveListener struct {
	net.Listener
}

func (l *keepAliveListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetKeepAlive(true)
		_ = tcp.SetKeepAlivePeriod(30 * time.Second)
	}
	return conn, nil
}

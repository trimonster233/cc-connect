package codex

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os/exec"
	"sync"
	"time"
)

// commandConn carries raw WebSocket bytes through a trusted administrator script.
// Close kills only the local proxy, never the remote shared App Server.
type commandConn struct {
	reader io.ReadCloser
	writer io.WriteCloser
	cmd    *exec.Cmd
	once   sync.Once
}

func newCommandConn(ctx context.Context, path string) (net.Conn, error) {
	cmd := exec.CommandContext(ctx, path)
	reader, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	writer, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, fmt.Errorf("codex: connect script: %w", err)
	}
	conn := &commandConn{reader: reader, writer: writer, cmd: cmd}
	go func() {
		data, err := io.ReadAll(io.LimitReader(stderr, 8192))
		if len(data) > 0 {
			slog.Warn("codex: connect script stderr", "message", string(data))
		}
		if err != nil {
			slog.Debug("codex: read proxy stderr", "error", err)
		}
	}()
	return conn, nil
}
func (c *commandConn) Read(p []byte) (int, error)  { return c.reader.Read(p) }
func (c *commandConn) Write(p []byte) (int, error) { return c.writer.Write(p) }
func (c *commandConn) Close() error {
	c.once.Do(func() {
		_ = c.writer.Close()
		_ = c.reader.Close()
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		_ = c.cmd.Wait()
	})
	return nil
}
func (c *commandConn) LocalAddr() net.Addr  { return commandAddr("local") }
func (c *commandConn) RemoteAddr() net.Addr { return commandAddr("script") }
func (c *commandConn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}
func (c *commandConn) SetReadDeadline(t time.Time) error {
	return c.reader.(interface{ SetReadDeadline(time.Time) error }).SetReadDeadline(t)
}
func (c *commandConn) SetWriteDeadline(t time.Time) error {
	return c.writer.(interface{ SetWriteDeadline(time.Time) error }).SetWriteDeadline(t)
}

type commandAddr string

func (a commandAddr) Network() string { return "script" }
func (a commandAddr) String() string  { return string(a) }

package codex

import (
	"context"
	"io"
	"testing"
	"time"
)

func TestCommandConn_StaysAliveUntilSessionCloses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, err := newCommandConn(ctx, "/bin/cat")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 5)
	if _, err := io.ReadFull(conn, b); err != nil {
		t.Fatal(err)
	}
	if string(b) != "hello" {
		t.Fatal(string(b))
	}
	cancel()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := conn.Read(b); err == nil {
		t.Fatal("canceled proxy still readable")
	}
}

package kiroclient

import (
	"net/http"
	"testing"
	"time"
)

func TestNewHTTPClient_ResponseHeaderTimeout(t *testing.T) {
	tests := []struct {
		name string
		opts []HTTPClientOption
		want time.Duration
	}{
		{name: "default", want: DefaultResponseHeaderTimeout},
		{name: "override", opts: []HTTPClientOption{WithResponseHeaderTimeout(90 * time.Second)}, want: 90 * time.Second},
		{name: "no limit", opts: []HTTPClientOption{WithResponseHeaderTimeout(0)}, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewHTTPClient(tt.opts...)
			tr, ok := c.httpClient.Transport.(*http.Transport)
			if !ok {
				t.Fatalf("transport is %T", c.httpClient.Transport)
			}
			if tr.ResponseHeaderTimeout != tt.want {
				t.Errorf("ResponseHeaderTimeout = %v, want %v", tr.ResponseHeaderTimeout, tt.want)
			}
		})
	}
}

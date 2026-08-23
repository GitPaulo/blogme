package discovery

import (
	"net/http"
	"testing"
)

func TestFramingDenied(t *testing.T) {
	tests := []struct {
		name   string
		header http.Header
		want   bool
	}{
		{name: "no headers", header: http.Header{}},
		{
			name:   "unrelated csp directives",
			header: http.Header{"Content-Security-Policy": {"default-src 'self'; img-src *"}},
		},
		{
			name:   "frame-ancestors wildcard",
			header: http.Header{"Content-Security-Policy": {"frame-ancestors *"}},
		},
		{
			name:   "frame-ancestors any https site",
			header: http.Header{"Content-Security-Policy": {"frame-ancestors https:"}},
		},
		{
			name:   "report-only is not enforced",
			header: http.Header{"Content-Security-Policy-Report-Only": {"frame-ancestors 'none'"}},
		},
		{
			name:   "x-frame-options allow-from is ignored by browsers",
			header: http.Header{"X-Frame-Options": {"ALLOW-FROM https://example.com"}},
		},
		{
			name:   "frame-ancestors none",
			header: http.Header{"Content-Security-Policy": {"frame-ancestors 'none'"}},
			want:   true,
		},
		{
			name:   "frame-ancestors names other sites",
			header: http.Header{"Content-Security-Policy": {"frame-ancestors 'self' https://jamiesimon.io"}},
			want:   true,
		},
		{
			name:   "frame-ancestors among other directives",
			header: http.Header{"Content-Security-Policy": {"default-src 'self'; frame-ancestors 'none'; img-src *"}},
			want:   true,
		},
		{
			name:   "one policy of several refuses",
			header: http.Header{"Content-Security-Policy": {"frame-ancestors *", "frame-ancestors 'self'"}},
			want:   true,
		},
		{
			name:   "directive name is case-insensitive",
			header: http.Header{"Content-Security-Policy": {"FRAME-ANCESTORS 'none'"}},
			want:   true,
		},
		{name: "x-frame-options deny", header: http.Header{"X-Frame-Options": {"DENY"}}, want: true},
		{
			name:   "x-frame-options sameorigin, lowercased and padded",
			header: http.Header{"X-Frame-Options": {" sameorigin "}},
			want:   true,
		},
		{
			name:   "x-frame-options repeated in one value",
			header: http.Header{"X-Frame-Options": {"SAMEORIGIN, SAMEORIGIN"}},
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := framingDenied(tt.header); got != tt.want {
				t.Errorf("framingDenied() = %v, want %v", got, tt.want)
			}
		})
	}
}

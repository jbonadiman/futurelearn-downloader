package fetch

import (
	"testing"
)

func TestValidateMasterURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"valid", "https://view.vzaar.com/12345/adaptive.m3u8", false},
		{"valid path", "https://view.vzaar.com/abc123/adaptive.m3u8", false},
		{"wrong scheme", "http://view.vzaar.com/12345/adaptive.m3u8", true},
		{"wrong host", "https://example.com/12345/adaptive.m3u8", true},
		{"wrong host partial", "https://view.vzaar.co/12345/adaptive.m3u8", true},
		{"invalid URL", "ht!tp://invalid", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMasterURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateMasterURL(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

func TestVzaarIDPattern(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{"numeric", "12345", false},
		{"alphanumeric", "abc123", false},
		{"lowercase", "abcdef", false},
		{"uppercase", "ABCDEF", false},
		{"mixed case", "AbC123", false},
		{"with dash", "123-456", true},
		{"with underscore", "123_456", true},
		{"with space", "123 456", true},
		{"with slash", "123/456", true},
		{"with dot", "123.456", true},
		{"empty", "", true},
		{"path traversal", "../../../etc/passwd", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			match := vzaarIDPattern.MatchString(tt.id)
			if match == tt.wantErr {
				t.Errorf("vzaarIDPattern.MatchString(%q) = %v, want %v", tt.id, match, !tt.wantErr)
			}
		})
	}
}

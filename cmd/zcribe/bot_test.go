package main

import (
	"reflect"
	"testing"
)

func TestParseCaptionFlags(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    map[string]string
		wantErr bool
	}{
		{"empty", "", map[string]string{}, false},
		{"blank", "   ", map[string]string{}, false},
		{"single", `name=Sunset`, map[string]string{"name": "Sunset"}, false},
		{"quoted with spaces", `name="Sunset over NoM"`, map[string]string{"name": "Sunset over NoM"}, false},
		{"quoted holds a comma", `refs="a,b" name=x`, map[string]string{"refs": "a,b", "name": "x"}, false},
		{"multiple flags", `name=x desc="a b" supply=3 decimals=0`,
			map[string]string{"name": "x", "desc": "a b", "supply": "3", "decimals": "0"}, false},
		{"key is lowercased", `NAME=x`, map[string]string{"name": "x"}, false},
		{"empty quoted value", `desc=""`, map[string]string{"desc": ""}, false},
		{"missing equals", `name`, nil, true},
		{"unterminated quote", `name="Sunset`, nil, true},
		{"extra whitespace between flags", `  name=x   desc=y  `, map[string]string{"name": "x", "desc": "y"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseCaptionFlags(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("parseCaptionFlags(%q): expected an error, got %v", c.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCaptionFlags(%q): unexpected error: %v", c.in, err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("parseCaptionFlags(%q) = %#v, want %#v", c.in, got, c.want)
			}
		})
	}
}

func TestPickMedia(t *testing.T) {
	newUpdate := func() tgUpdate {
		var u tgUpdate
		u.Message = &struct {
			MessageID int64 `json:"message_id"`
			From      struct {
				ID       int64  `json:"id"`
				Username string `json:"username"`
			} `json:"from"`
			Chat struct {
				ID   int64  `json:"id"`
				Type string `json:"type"`
			} `json:"chat"`
			Text    string `json:"text"`
			Caption string `json:"caption"`
			Photo   []struct {
				FileID   string `json:"file_id"`
				FileSize int64  `json:"file_size"`
			} `json:"photo"`
			Document *struct {
				FileID   string `json:"file_id"`
				FileName string `json:"file_name"`
				MimeType string `json:"mime_type"`
				FileSize int64  `json:"file_size"`
			} `json:"document"`
		}{}
		return u
	}

	t.Run("neither photo nor document errors", func(t *testing.T) {
		u := newUpdate()
		if _, _, _, _, err := pickMedia(u); err == nil {
			t.Fatal("expected an error when nothing is attached")
		}
	})

	t.Run("document is preferred and preserves its own mime and name", func(t *testing.T) {
		u := newUpdate()
		u.Message.Photo = append(u.Message.Photo, struct {
			FileID   string `json:"file_id"`
			FileSize int64  `json:"file_size"`
		}{FileID: "photo1", FileSize: 100})
		u.Message.Document = &struct {
			FileID   string `json:"file_id"`
			FileName string `json:"file_name"`
			MimeType string `json:"mime_type"`
			FileSize int64  `json:"file_size"`
		}{FileID: "doc1", FileName: "a.svg", MimeType: "image/svg+xml", FileSize: 42}

		id, name, mime, size, err := pickMedia(u)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if id != "doc1" || name != "a.svg" || mime != "image/svg+xml" || size != 42 {
			t.Fatalf("got (%q, %q, %q, %d), want the document's own fields", id, name, mime, size)
		}
	})

	t.Run("photo falls back to the largest size as image/jpeg", func(t *testing.T) {
		u := newUpdate()
		u.Message.Photo = []struct {
			FileID   string `json:"file_id"`
			FileSize int64  `json:"file_size"`
		}{
			{FileID: "small", FileSize: 10},
			{FileID: "large", FileSize: 1000},
		}
		id, name, mime, size, err := pickMedia(u)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if id != "large" || name != "" || mime != "image/jpeg" || size != 1000 {
			t.Fatalf("got (%q, %q, %q, %d), want the last (largest) photo size as image/jpeg", id, name, mime, size)
		}
	})
}

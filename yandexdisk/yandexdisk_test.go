package yandexdisk

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type fakeDisk struct {
	mu             sync.Mutex
	srv            *httptest.Server
	uploaded       []byte
	published      []string
	publishQueries []string
	publishBodies  [][]byte
	folderPUTs     int
	failPublish    bool
}

func newFakeDisk(t *testing.T) *fakeDisk {
	t.Helper()
	f := &fakeDisk{}
	mux := http.NewServeMux()
	mux.HandleFunc("/resources", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "OAuth test-token" {
			http.Error(w, `{"message":"bad token","error":"UnauthorizedError"}`, http.StatusForbidden)
			return
		}
		switch r.Method {
		case http.MethodPut:
			f.mu.Lock()
			f.folderPUTs++
			f.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"public_url": "https://disk.yandex.ru/i/testXYZ"})
		}
	})
	mux.HandleFunc("/resources/upload", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "OAuth test-token" {
			http.Error(w, `{"message":"bad token","error":"UnauthorizedError"}`, http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"href": f.srv.URL + "/upload-target"})
	})
	mux.HandleFunc("/upload-target", func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.uploaded = data
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("/resources/publish", func(w http.ResponseWriter, r *http.Request) {
		if f.failPublish {
			http.Error(w, `{"message":"disk quota exceeded","error":"DiskResourceDoesNotExistError"}`, http.StatusPreconditionFailed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.published = append(f.published, r.URL.Query().Get("path"))
		f.publishQueries = append(f.publishQueries, r.URL.RawQuery)
		f.publishBodies = append(f.publishBodies, body)
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func newTestClient(f *fakeDisk) *Client {
	return &Client{token: "test-token", base: f.srv.URL, http: f.srv.Client()}
}

func TestCreateDocHappyPath(t *testing.T) {
	f := newFakeDisk(t)
	c := newTestClient(f)

	url, err := c.CreateDoc(context.Background(), "Alice's phone")
	if err != nil {
		t.Fatalf("CreateDoc: %v", err)
	}
	if url != "https://disk.yandex.ru/i/testXYZ" {
		t.Fatalf("url = %q", url)
	}
	if f.folderPUTs != 1 {
		t.Fatalf("folderPUTs = %d, want 1", f.folderPUTs)
	}
	if len(f.uploaded) == 0 {
		t.Fatal("no document body was uploaded")
	}
	if len(f.published) != 1 || !strings.Contains(f.published[0], "openflux/Alices-phone-") {
		t.Fatalf("published = %v, want one path under /openflux with a sanitized name", f.published)
	}
}

// TestCreateDocPublishesWithWriteAccessForEveryone locks in the fix for a
// real production bug: publishing a document with Yandex Disk's own
// defaults grants the public link view-only access, which transport/yandex
// can't use (no editor_config.token/document.key -- no collaborative
// session to open). Without allow_address_access=true and an explicit
// accesses:[{macros:["all"], rights:["write"]}], a generated document
// connects to nothing.
func TestCreateDocPublishesWithWriteAccessForEveryone(t *testing.T) {
	f := newFakeDisk(t)
	c := newTestClient(f)

	if _, err := c.CreateDoc(context.Background(), "x"); err != nil {
		t.Fatalf("CreateDoc: %v", err)
	}
	if len(f.publishQueries) != 1 {
		t.Fatalf("publish calls = %d, want 1", len(f.publishQueries))
	}
	if !strings.Contains(f.publishQueries[0], "allow_address_access=true") {
		t.Fatalf("publish query = %q, want allow_address_access=true", f.publishQueries[0])
	}

	var body publicSettingsRequest
	if err := json.Unmarshal(f.publishBodies[0], &body); err != nil {
		t.Fatalf("publish body is not valid JSON: %v (%s)", err, f.publishBodies[0])
	}
	accesses := body.PublicSettings.Accesses
	if len(accesses) != 1 {
		t.Fatalf("accesses = %+v, want exactly one entry", accesses)
	}
	if len(accesses[0].Macros) != 1 || accesses[0].Macros[0] != "all" {
		t.Fatalf("macros = %v, want [\"all\"]", accesses[0].Macros)
	}
	if len(accesses[0].Rights) != 1 || accesses[0].Rights[0] != "write" {
		t.Fatalf("rights = %v, want [\"write\"] (view-only can't produce a collaborative session)", accesses[0].Rights)
	}
}

func TestCreateDocUploadsAValidZip(t *testing.T) {
	f := newFakeDisk(t)
	c := newTestClient(f)

	if _, err := c.CreateDoc(context.Background(), "x"); err != nil {
		t.Fatalf("CreateDoc: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(f.uploaded), int64(len(f.uploaded)))
	if err != nil {
		t.Fatalf("uploaded document is not a valid zip: %v", err)
	}
	var names []string
	for _, zf := range zr.File {
		names = append(names, zf.Name)
	}
	for _, want := range []string{"[Content_Types].xml", "_rels/.rels", "word/document.xml"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("uploaded docx is missing %q, has %v", want, names)
		}
	}
}

func TestCreateDocRequiresToken(t *testing.T) {
	c := New("")
	if _, err := c.CreateDoc(context.Background(), "x"); err == nil {
		t.Fatal("expected an error with no token configured")
	}
}

func TestCreateDocFailsWithBadToken(t *testing.T) {
	f := newFakeDisk(t)
	c := &Client{token: "wrong-token", base: f.srv.URL, http: f.srv.Client()}

	if _, err := c.CreateDoc(context.Background(), "x"); err == nil {
		t.Fatal("expected an error with a rejected token")
	}
}

func TestCreateDocSurfacesPublishFailure(t *testing.T) {
	f := newFakeDisk(t)
	f.failPublish = true
	c := newTestClient(f)

	_, err := c.CreateDoc(context.Background(), "x")
	if err == nil {
		t.Fatal("expected an error when publish fails")
	}
	if !strings.Contains(err.Error(), "publish") {
		t.Fatalf("error = %v, want it to mention publish", err)
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"":                      "client",
		"  ":                    "client",
		"Alice's phone":         "Alices-phone",
		"../../etc/passwd":      "etcpasswd",
		"a b/c\\d?e&f#g":        "a-bcdefg",
		strings.Repeat("x", 60): strings.Repeat("x", 40),
	}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBlankDocxIsAValidZipWithNoPathTraversal(t *testing.T) {
	data := blankDocx()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("blankDocx is not a valid zip: %v", err)
	}
	if len(zr.File) != 3 {
		t.Fatalf("got %d files, want 3", len(zr.File))
	}
}

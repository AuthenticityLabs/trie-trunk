package serve

import (
	"net/http"
	"regexp"
)

// Vite's default asset names contain an eight-character content hash.
// Unversioned files, especially the HTML shell, must still revalidate.
var versionedAsset = regexp.MustCompile(`^/assets/[^/]+-[A-Za-z0-9_-]{8}\.[A-Za-z0-9]+$`)

func staticFiles(root string) http.Handler {
	files := http.FileServer(http.Dir(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		policy := "no-cache"
		if versionedAsset.MatchString(r.URL.Path) {
			policy = "public, max-age=31536000, immutable"
		}
		files.ServeHTTP(&staticResponse{ResponseWriter: w, policy: policy}, r)
	})
}

type staticResponse struct {
	http.ResponseWriter
	policy      string
	wroteHeader bool
}

func (w *staticResponse) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	policy := w.policy
	if (status < 200 || status >= 300) && status != http.StatusNotModified {
		// A missing asset must not become an immutable, year-long cached 404.
		policy = "no-store"
	}
	w.Header().Set("Cache-Control", policy)
	w.ResponseWriter.WriteHeader(status)
}

func (w *staticResponse) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

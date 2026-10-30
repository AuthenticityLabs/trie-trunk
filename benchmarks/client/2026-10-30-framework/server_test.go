// Copy beside the selected revision's seed-demo main.go for this isolated test.
// Add inputHash = sha256.New() and hash the canonical values before l.Encode:
// fmt.Fprintf(inputHash, "%s/%s/%s:%d:", service, resource, branch.Name(), at.UnixMilli())
// for _, v := range values { fmt.Fprintf(inputHash, "%d:%x;", v.State, v.Bytes) }
// The digest verifies inputs across layouts; it is not a store-file digest.
package main
import("context";"encoding/hex";"encoding/json";"fmt";"net/http";"net/http/httptest";"os";"path/filepath";"testing";"time";"trie-trunk/go/server/cache")
func TestFrameworkBenchmarkServer(t *testing.T){
 out:=os.Getenv("FRAMEWORK_BENCH_OUT");if out==""{t.Skip("isolated benchmark only")};os.MkdirAll(out,0700)
 end:=time.Date(2026,10,30,0,0,0,0,time.UTC);clock:=func()time.Time{return end.Add(24*time.Hour)}
 r,err:=seedWithClock(filepath.Join(out,"store"),50000,30,end,clock);if err!=nil{t.Fatal(err)}
 os.WriteFile(filepath.Join(out,"input.sha256"),[]byte(hex.EncodeToString(inputHash.Sum(nil))),0600)
 e,err:=cache.OpenEndpoint(cache.Config{Root:filepath.Join(out,"store"),Cache:r.Cache,Tenant:r.Tenant,Now:clock});if err!=nil{t.Fatal(err)};defer e.Close()
 mux:=http.NewServeMux();mux.Handle("/bench/",http.StripPrefix("/bench/",http.FileServer(http.Dir(filepath.Join(out,"web")))))
 api:=e.Handler("");mux.HandleFunc("/",func(w http.ResponseWriter,q *http.Request){if q.URL.Path=="/"{w.Header().Set("Content-Type","text/html");fmt.Fprint(w,"<title>Isolated Historical Benchmark</title>");return};api.ServeHTTP(w,q)})
 server:=httptest.NewServer(mux);defer server.Close()
 metadata,_:=json.Marshal(map[string]any{"origin":server.URL,"fixture":r});os.WriteFile(filepath.Join(out,"server.json"),metadata,0600)
 t.Log("READY",server.URL)
 for{if _,err:=os.Stat(filepath.Join(out,"stop"));err==nil{return};select{case <-context.Background().Done():return;case <-time.After(time.Second):}}
}

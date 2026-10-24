package cache

import "testing"

// Production versus frozen optimized experiment; definitions and values are
// prepared before timing. This isolates promotion overhead from IO and search.
func BenchmarkProductionIntegers(b *testing.B) {
	for _, shape := range []string{"full", "sparse"} {
		for _, mode := range []string{"16", "32"} {
			d := compactTestDefinition("U16", "I16", "U32", "I32", "U64", "I64", "U16", "U32", "U8", "STRING", "STRING")
			oracle, e := compileSeparateWidthExperiment(d, mode == "16")
			if e != nil {
				b.Fatal(e)
			}
			d.PIL = 1
			d.AdaptiveIntegers = mode
			l, e := Compile(d)
			if e != nil {
				b.Fatal(e)
			}
			values := make([][]Value, 1024)
			frames := make([][]byte, 1024)
			total := 0
			for i := range values {
				v := []Value{{}, Uint(uint64(i), 2), Uint(uint64(int64(i-512)), 2), Uint(uint64(i*1000), 4), Uint(uint64(int64(i-512)), 4), Uint(uint64(i)*100000, 8), Uint(uint64(int64(i-512)), 8), Uint(uint64(i%256), 2), Uint(uint64(i), 4), Uint(uint64(i%256), 1), Text("request complete"), Text("worker-01")}
				if shape == "sparse" {
					for j := range v {
						if j != 1 && j != 6 {
							v[j] = Value{}
						}
					}
				}
				values[i] = v
				frames[i], e = l.Encode(nil, v)
				if e != nil {
					b.Fatal(e)
				}
				total += len(frames[i])
			}
			for _, path := range []string{"production_encode", "optimized_encode", "production_scan", "optimized_scan"} {
				b.Run(shape+"/"+mode+"/"+path, func(b *testing.B) {
					var dst []byte
					var f Frame
					dst, _ = l.Encode(dst, values[0])
					_ = l.Scan(frames[0], &f)
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						switch path {
						case "production_encode":
							dst, e = l.Encode(dst, values[i%1024])
						case "optimized_encode":
							dst, e = oracle.encodeReuse(dst, values[i%1024])
						case "production_scan":
							e = l.Scan(frames[i%1024], &f)
						case "optimized_scan":
							e = oracle.scan(frames[i%1024], &f)
						}
						if e != nil {
							b.Fatal(e)
						}
					}
					b.ReportMetric(float64(total)/1024, "wire-B/event")
				})
			}
		}
	}
}

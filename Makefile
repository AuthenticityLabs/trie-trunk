.PHONY: build test rust web wasm

build: rust web
	cp rust/target/release/trunk-search trunk-search
	cd go && go build -o ../trunk ./cmd/trunk

rust:
	cargo build --release --locked --manifest-path rust/Cargo.toml --target-dir rust/target

web:
	npm --prefix client ci
	npm --prefix client run build

test: rust
	cargo test --locked --manifest-path pil/Cargo.toml --target-dir pil/target
	cargo test --locked --manifest-path rust/Cargo.toml --target-dir rust/target
	cd go && PATH="$(CURDIR)/rust/target/release:$$PATH" go test -count=1 ./...

wasm:
	npm --prefix client run wasm

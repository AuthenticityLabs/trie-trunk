.PHONY: build test rust web wasm site schema desktop

build: rust web
	cp rust/target/release/trunk-search trunk-search
	cd go && CGO_ENABLED=1 go build -o ../trunk ./cmd/trunk

rust:
	cargo build --release --locked --manifest-path rust/Cargo.toml --target-dir rust/target

web:
	npm --prefix client ci
	npm --prefix client run build

test: rust
	cd ../pill-packer/go && go test ./codec
	cargo test --locked --all-features --manifest-path ../pill-packer/rust/Cargo.toml --target-dir ../pill-packer/rust/target
	node scripts/check-definition-schema.mjs
	cargo test --locked --manifest-path rust/Cargo.toml --target-dir rust/target
	cargo test --locked --manifest-path rust/uuid-index/Cargo.toml
	cd go && PATH="$(CURDIR)/rust/target/release:$$PATH" go test -count=1 ./...

wasm:
	npm --prefix client run wasm

site:
	node site/build.mjs

schema:
	node scripts/check-definition-schema.mjs

desktop:
	python3 desktop/build.py

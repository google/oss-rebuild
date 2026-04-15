The Sysgraph SDK provides tools and libraries to interact with the Sysgraph API

If you modified the Sysgraph protos, you can regenerate the `pb.go` files:

```bash
go generate ./pkg/sysgraph/proto/sysgraph/...
```

**Note:** This requires a relatively new `protoc` version to support proto edition 2024 (e.g., `protoc --version` returning `libprotoc 33.4` or newer).

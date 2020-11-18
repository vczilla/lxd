export CGO_CFLAGS="-I/data/data/com.termux/files/home/go/deps/raft/include/ -I/data/data/com.termux/files/home/go/deps/dqlite/include/ -I$PREFIX/include -D__ANDROID_API__=29"
export CGO_LDFLAGS="-L/data/data/com.termux/files/home/go/deps/raft/.libs -L/data/data/com.termux/files/home/go/deps/dqlite/.libs/ -L$PREFIX/lib -fuse-ld=lld"
export LD_LIBRARY_PATH="/data/data/com.termux/files/home/go/deps/raft/.libs/:/data/data/com.termux/files/home/go/deps/dqlite/.libs/"
export CGO_LDFLAGS_ALLOW="-Wl,-wrap,pthread_create"

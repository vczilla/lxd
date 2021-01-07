#export CGO_CFLAGS="-I${HOME}/go/deps/raft/include/ -I${HOME}/go/ deps/dqlite/include/"
#export CGO_LDFLAGS="-L${HOME}/go/deps/raft/.libs -L${HOME}/go/deps/dqlite/.libs/"
#export LD_LIBRARY_PATH="${HOME}/go/deps/raft/.libs/:${HOME}/go/deps/dqlite/.libs/"
# export CGO_LDFLAGS_ALLOW="-Wl,-wrap,pthread_create"

export GOPATH=${HOME}/go
export CGO_CFLAGS="-I${GOPATH}/include -U__ANDROID_API__ -D__ANDROID_API__=29"
export CGO_LDFLAGS="-L${GOPATH}/lib -Wl,-R\$ORIGIN/../lib:/system/lib64 -Wl,--as-needed -fuse-ld=lld"
export CGO_LDFLAGS_ALLOW="-Wl,-wrap,pthread_create -Wl,--as-needed -fuse-ld=lld"

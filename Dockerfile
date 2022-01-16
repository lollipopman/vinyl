FROM golang:1.13
WORKDIR /go/src/github.com/lollipopman/vinyl/
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a -o vinyl ./cmd/vinyl

FROM alpine:3.11  
RUN apk --no-cache add diffutils
WORKDIR /tmp
COPY --from=0 /go/src/github.com/lollipopman/vinyl/vinyl /usr/local/bin/vinyl
ENTRYPOINT ["/usr/local/bin/vinyl"] 

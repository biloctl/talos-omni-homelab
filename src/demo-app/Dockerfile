# ---- build stage: full Go toolchain (~800MB), thrown away ----
FROM golang:1.23 AS build
WORKDIR /src
COPY go.mod ./
COPY main.go ./
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-X main.version=${VERSION}" -o /demo-app .

# ---- final stage: just the static binary, no OS, no shell ----
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /demo-app /demo-app
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/demo-app"]

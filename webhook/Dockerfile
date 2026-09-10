# Multi-stage: the build stage has the full Go toolchain, the runtime image
# has none of it. distroless/static's nonroot variant has no shell, no
# package manager, and runs as uid 65532 by default -- there's no attack
# surface here beyond the single static binary itself.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# CGO disabled + a static binary is what makes the distroless/static base
# (no libc at all) work in the first place.
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/webhook .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/webhook /webhook
USER nonroot:nonroot
ENTRYPOINT ["/webhook"]

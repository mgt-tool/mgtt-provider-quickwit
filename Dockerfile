FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/provider .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/provider /bin/provider
COPY manifest.yaml /manifest.yaml
COPY types /types
ENTRYPOINT ["/bin/provider"]

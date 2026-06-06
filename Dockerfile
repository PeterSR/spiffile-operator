FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /spiffile-operator .

FROM gcr.io/distroless/static:nonroot
COPY --from=build /spiffile-operator /spiffile-operator
USER 65532:65532
ENTRYPOINT ["/spiffile-operator"]

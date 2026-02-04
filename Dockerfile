FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /kube-restarter .

FROM gcr.io/distroless/static:nonroot
COPY --from=build /kube-restarter /kube-restarter
USER nonroot:nonroot
ENTRYPOINT ["/kube-restarter"]

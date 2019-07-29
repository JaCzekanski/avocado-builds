FROM golang:1.12-alpine as build
WORKDIR /app
RUN apk add --no-cache git ca-certificates
COPY . .
RUN CGO_ENABLED=0 go build .


FROM scratch
WORKDIR /app
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /app/avocado-builds .
COPY --from=build /app/template ./template/
ENTRYPOINT ["./avocado-builds"]

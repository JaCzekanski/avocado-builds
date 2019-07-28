FROM golang:1.12-alpine as build
WORKDIR /app
COPY . .
RUN apk add --no-cache git
RUN CGO_ENABLED=0 go build .


FROM scratch
WORKDIR /app
COPY --from=build /app/avocado-builds .
COPY --from=build /app/template ./template/
ENTRYPOINT ["./avocado-builds"]

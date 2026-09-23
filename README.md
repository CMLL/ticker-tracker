# Stock ticker average running

Small go server that pulls the NDAYS from a SYMBOL and calculates its average value.

### Run

```bash
touch .env
echo "API_KEY={KEY}\nTICKER={TICKER}\nNDAYS={days}" > .env

go run cmd/server/main.go

curl -X GET http://localhost:8080/average
```

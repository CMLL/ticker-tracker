# Stock ticker average running

Small go server that pulls the NDAYS from a SYMBOL and calculates its average value.

### Docker Compose

```bash
touch .env
echo "API_KEY={KEY}\nTICKER={TICKER}\nNDAYS={days}" > .env

docker compose up -d web
```

### Kubernetes

```bash
# Modifyt the secret value in k8s/secret.yaml before
kubectl apply -f k8s/secret.yaml
kubectl apply -f k8s/manifest.yaml

curl http://{INGRESS_HOST}/average
```

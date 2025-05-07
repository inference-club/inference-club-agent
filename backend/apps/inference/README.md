## Trigger inference with flux

```
cd backend && poetry run python -m apps.inference.test_flux
```

```
curl --request POST \
     --url http://localhost:8001/v1/infer \
     --header 'accept: application/json' \
     --header 'content-type: application/json' \
     --data '
{
  "prompt": "A beautiful image of a cat",
  "height": 1024,
  "width": 1024,
  "cfg_scale": 5,
  "mode": "base",
  "samples": 1,
  "seed": 0,
  "steps": 50
}
'
```


## Port forward to PC running flux API

```
ssh -L 8001:192.168.5.173:8001 brian@192.168.5.173
```
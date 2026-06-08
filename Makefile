COMPOSE      := docker compose
COMPOSE_DEV  := docker compose -f docker-compose.dev.yml

.PHONY: refresh-dev refresh-prod

# Rebuild and recreate the local dev container (club-host-dev, :8090, .env.dev).
refresh-dev:
	$(COMPOSE_DEV) up -d --build

# Rebuild and recreate the prod container (club-host, tailnet, .env).
refresh-prod:
	$(COMPOSE) up -d --build

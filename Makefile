# Local development ------------------------------------------------------

.PHONY: test build up down e2e loadtest deploy push destroy demo-deploy demo-update demo-destroy

test:
	go vet ./...
	go test ./...

build:
	cd web && npm run build
	go build ./...

up:
	docker compose up -d --build

down:
	docker compose down

e2e:
	./scripts/e2e.sh

loadtest:
	k6 run loadtest/game_flow.js

# AWS deploy window -------------------------------------------------------
# Requires: aws cli with credentials, terraform, and TF_VAR_db_password.
# The stack costs real money while up; `make destroy` when done.

AWS_REGION ?= us-east-1
TF = terraform -chdir=deploy/terraform

deploy:
	$(TF) init -input=false
	$(TF) apply -auto-approve -input=false
	@$(MAKE) --no-print-directory push TF_DIR=deploy/terraform PLATFORM=linux/amd64
	aws ecs update-service --region $(AWS_REGION) --cluster arena --service api --force-new-deployment > /dev/null
	aws ecs update-service --region $(AWS_REGION) --cluster arena --service worker --force-new-deployment > /dev/null
	@echo "live at: $$(terraform -chdir=deploy/terraform output -raw url)"

# Build and push both images to whichever stack's ECR repos TF_DIR names.
# Everything runs in one shell so the registry host is resolved at run
# time; expanding it with make's $(shell ...) picks up an empty value
# because the repos may not exist when the recipe is expanded.
push:
	@set -eu; \
	api=$$(terraform -chdir=$(TF_DIR) output -raw ecr_api); \
	worker=$$(terraform -chdir=$(TF_DIR) output -raw ecr_worker); \
	registry=$${api%%/*}; \
	echo "pushing to $$registry"; \
	aws ecr get-login-password --region $(AWS_REGION) \
		| docker login --username AWS --password-stdin "$$registry"; \
	docker build --platform $(PLATFORM) --provenance=false --target api -t "$$api:latest" .; \
	docker build --platform $(PLATFORM) --provenance=false --target worker -t "$$worker:latest" .; \
	docker push "$$api:latest"; \
	docker push "$$worker:latest"

destroy:
	$(TF) destroy -auto-approve

# Always-on demo box ------------------------------------------------------
# One small ARM instance running the same containers against real SQS,
# for the public link. Roughly $10/month, versus ~$60 for the fleet above.

TFDEMO = terraform -chdir=deploy/demo

demo-deploy:
	$(TFDEMO) init -input=false
	$(TFDEMO) apply -auto-approve -input=false \
		-target=aws_ecr_repository.api -target=aws_ecr_repository.worker -target=aws_sqs_queue.moves
	@$(MAKE) --no-print-directory push TF_DIR=deploy/demo PLATFORM=linux/arm64
	$(TFDEMO) apply -auto-approve -input=false
	@echo "demo live at: $$($(TFDEMO) output -raw url)"

# Ship new code to the running demo box without recreating it.
demo-update:
	@$(MAKE) --no-print-directory push TF_DIR=deploy/demo PLATFORM=linux/arm64
	@set -eu; \
	api=$$($(TFDEMO) output -raw ecr_api); \
	registry=$${api%%/*}; \
	aws ssm send-command --region $(AWS_REGION) \
		--instance-ids $$($(TFDEMO) output -raw instance_id) \
		--document-name AWS-RunShellScript \
		--parameters "commands=[\"cd /opt/arena && aws ecr get-login-password --region $(AWS_REGION) | docker login --username AWS --password-stdin $$registry && docker compose pull && docker compose up -d\"]" \
		--output text --query 'Command.CommandId'

demo-destroy:
	$(TFDEMO) destroy -auto-approve

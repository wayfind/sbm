VERSION := $(shell grep 'sbmVersion' cmd/info.go | head -1 | sed 's/.*"\(.*\)".*/\1/')
SINGBOX_VERSION := 1.13.3

build:
	GOOS=linux GOARCH=amd64 go build -o sbm .

# Download pinned sing-box release
fetch-singbox:
	curl -fsSL -o /tmp/sing-box.tar.gz \
	  "https://github.com/SagerNet/sing-box/releases/download/v$(SINGBOX_VERSION)/sing-box-$(SINGBOX_VERSION)-linux-amd64.tar.gz"
	tar -xf /tmp/sing-box.tar.gz -C /tmp --strip-components=1 "sing-box-$(SINGBOX_VERSION)-linux-amd64/sing-box"
	mv /tmp/sing-box sing-box-linux-amd64
	rm /tmp/sing-box.tar.gz
	@echo "sing-box $(SINGBOX_VERSION) → sing-box-linux-amd64"

# Publish sbm + sing-box + install.sh to CF R2
publish: build fetch-singbox
	cd cf-worker && npx wrangler r2 object put sbm-releases/releases/sbm/v$(VERSION)/sbm-linux-amd64 --file=../sbm --remote
	cd cf-worker && npx wrangler r2 object put sbm-releases/releases/sing-box/$(SINGBOX_VERSION)/sing-box-linux-amd64 --file=../sing-box-linux-amd64 --remote
	@echo '{"sbm":"$(VERSION)","sing_box":"$(SINGBOX_VERSION)"}' > /tmp/latest.json
	cd cf-worker && npx wrangler r2 object put sbm-releases/releases/latest.json --file=/tmp/latest.json --remote
	cd cf-worker && npx wrangler r2 object put sbm-releases/releases/install.sh --file=../scripts/install.sh --remote
	@echo "Published sbm v$(VERSION) + sing-box $(SINGBOX_VERSION) to R2"

# Deploy Worker
deploy-worker:
	cd cf-worker && npx wrangler deploy

# Deploy sbm to metask_gia
deploy: build
	ssh root@67.230.165.100 'mkdir -p ~/sing-box-manager'
	scp sbm root@67.230.165.100:~/sing-box-manager/sbm

# Deploy sbm to all nodes
deploy-all: build
	@for host in root@67.230.165.100 david@175.27.169.180; do \
	  echo "deploying to $$host..."; \
	  scp sbm $$host:~/sbm && echo "  done" || echo "  failed"; \
	done

clean:
	rm -f sbm sing-box-linux-amd64

.PHONY: build fetch-singbox publish deploy-worker deploy deploy-all clean

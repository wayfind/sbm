VERSION := $(shell grep 'Version =' internal/version.go | sed 's/.*"\(.*\)".*/\1/')
SINGBOX_VERSION := 1.13.3
SINGBOX_TAGS := with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_acme,with_clash_api,with_v2ray_api,with_tailscale,with_ccm,with_ocm

build:
	GOOS=linux GOARCH=amd64 go build -o sbm .

# Build sing-box from source with required tags (including with_v2ray_api)
fetch-singbox:
	@if [ ! -d /tmp/sing-box-src ]; then \
	  git clone --depth 1 --branch v$(SINGBOX_VERSION) https://github.com/SagerNet/sing-box.git /tmp/sing-box-src; \
	fi
	cd /tmp/sing-box-src && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
	  go build -tags "$(SINGBOX_TAGS)" -trimpath \
	  -ldflags "-s -w -X 'github.com/sagernet/sing-box/constant.Version=$(SINGBOX_VERSION)'" \
	  -o $(CURDIR)/sing-box-linux-amd64 ./cmd/sing-box
	@echo "sing-box $(SINGBOX_VERSION) (custom build, tags: $(SINGBOX_TAGS)) → sing-box-linux-amd64"

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

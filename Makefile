build:
	GOOS=linux GOARCH=amd64 go build -o sbm .

deploy: build
	ssh root@67.230.165.100 'mkdir -p ~/sing-box-manager'
	scp sbm root@67.230.165.100:~/sing-box-manager/sbm

clean:
	rm -f sbm

.PHONY: build deploy clean

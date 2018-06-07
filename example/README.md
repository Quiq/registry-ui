# Docker Registry UI Example

Start the local Docker Registry and UI.

```bash
$ docker-compose up -d
```

As an example, push the docker-registry-ui image to the local Docker Registry.

```bash
$ docker tag quiq/docker-registry-ui localhost/quiq/docker-registry-ui
$ docker push localhost/quiq/docker-registry-ui
The push refers to repository [localhost:5000/quiq/docker-registry-ui]
ab414a599bf8: Pushed
a8da33adf86e: Pushed
71a0e0a972a7: Pushed
96dc74eb5456: Pushed
ac362bf380d0: Pushed
04a094fe844e: Pushed
latest: digest: sha256:d88c1ca40986a358e59795992e87e364a0b3b97833aade5abcd79dda0a0477e8 size: 1571
```

Then you will find the pushed repository 'quiq/docker-registry-ui' in the following URL.
http://localhost/ui/quiq/docker-registry-ui

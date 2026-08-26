# Vécu - image serveur self-hosted.
# Le merge server-side passe par le binaire git (git merge-tree --write-tree, git ≥ 2.38).
# Source: https://git-scm.com/docs/git-merge-tree
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
# appdist/ n'existe que dans le depot de travail : le depot public l'exclut
# (les binaires y sont signes avec la cle privee de release). Sans ce mkdir,
# le COPY --from plus bas echoue sur un clone public et `docker compose up`
# ne demarre jamais. Sur le depot prive, mkdir -p ne fait rien.
RUN mkdir -p /src/appdist
RUN CGO_ENABLED=0 go build -o /out/vecu-server ./server

FROM alpine:3.22
# alpine 3.22 embarque git ≥ 2.38 (requis pour merge-tree --write-tree)
RUN apk add --no-cache git && git version
COPY --from=build /out/vecu-server /usr/local/bin/vecu-server
# Artefacts de release de l'app de bureau (manifeste scellé + binaires par cible),
# committés dans appdist/ par `make release`. Embarqués dans l'image → servis par
# /app/manifest et /app/download après le même git push, sans volume persistant.
COPY --from=build /src/appdist /appdist
# Pas d'instruction VOLUME : Railway la refuse (les volumes s'attachent au
# service), et `docker run -v vecu-data:/data` n'en a pas besoin non plus.
ENV VECU_DATA=/data
ENV VECU_APPDIST=/appdist
EXPOSE 8080
ENTRYPOINT ["vecu-server"]

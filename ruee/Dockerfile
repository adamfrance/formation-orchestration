# syntax=docker/dockerfile:1
# Une seule recette pour toutes les briques de Ruée : l'argument SERVICE choisit le programme
#   docker build --build-arg SERVICE=salle-attente --build-arg VERSION=1.0.0 -t localhost:5001/ruee/salle-attente:1.0.0 .
# (La première ligne choisit la syntaxe de Dockerfile la plus récente de BuildKit, le moteur de construction.)

# 1. Construction : l'outillage Go complet (compilateur, bibliothèques), qui ne part pas en production.
# --platform=$BUILDPLATFORM : on compile sur l'architecture de la machine qui construit (rapide, sans émulation)
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS construction
# Le dossier de travail dans l'image de construction
WORKDIR /src
# Les dépendances d'abord : cette couche reste en cache tant que go.mod et go.sum ne changent pas
COPY go.mod go.sum ./
# Le cache des modules est monté pendant la construction seulement : il n'alourdit pas l'image
RUN --mount=type=cache,target=/go/pkg/mod go mod download
# Puis le code source (une modification du code ne refait que les étapes qui suivent)
COPY cmd ./cmd
COPY internal ./internal
# L'argument vient après le téléchargement : les briques partagent les couches précédentes
ARG SERVICE
# L'architecture visée (amd64 pour les serveurs Intel et AMD, arm64 pour Graviton ou un Mac), fournie par BuildKit
ARG TARGETOS TARGETARCH
# Un programme autonome (CGO_ENABLED=0 : aucune bibliothèque système), sans chemins locaux ni symboles de débogage
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /service ./cmd/${SERVICE}

# 2. L'image finale : le programme seul, sans shell ni gestionnaire de paquets, avec un utilisateur sans droits
# (distroless : quelques fichiers indispensables, comme les certificats et le fuseau horaire, et rien d'autre)
FROM gcr.io/distroless/static-debian13:nonroot
ARG SERVICE
ARG VERSION=dev
# La version, lisible par le programme (elle apparaît dans chaque ligne du journal)
ENV VERSION=${VERSION}
# Les étiquettes standard de l'OCI : nom et version de l'image
LABEL org.opencontainers.image.title="ruee-${SERVICE}" org.opencontainers.image.version="${VERSION}"
# Seul le programme compilé passe de l'étape 1 à l'image finale
COPY --from=construction /service /service
# L'utilisateur « nonroot » de distroless, par son numéro (65532) : une faille dans le programme ne donne pas les
# droits d'administrateur, et Kubernetes peut vérifier qu'il ne s'agit pas de root (avec un nom, il ne le peut pas)
USER 65532:65532
# La commande lancée au démarrage du conteneur
ENTRYPOINT ["/service"]

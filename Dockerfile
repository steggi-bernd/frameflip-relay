# Zwei Stufen. Was am Ende laeuft, ist ein einzelnes statisches Binary in einem
# leeren Image - keine Shell, kein Paketmanager, kein Betriebssystem darunter.
#
# Das ist nicht Sparsamkeit um ihrer selbst willen: Ein oeffentlich erreichbarer
# Dienst ohne Laufzeitumgebung hat nichts, was gepatcht werden muesste, und nichts,
# worin sich jemand nach einem Einbruch umsehen koennte.

FROM golang:1.27-alpine AS build

WORKDIR /src

# Erst die Modulliste, dann der Rest: So bleibt der Download der Abhaengigkeiten
# im Cache, solange sich go.mod nicht aendert.
COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./

# CGO aus: sonst haengt das Binary an der libc des Build-Images und liefe in einem
# leeren Container nicht. -trimpath haelt lokale Pfade aus dem Ergebnis heraus.
ENV CGO_ENABLED=0
RUN go vet ./... && go test -count=1 ./... && \
    go build -trimpath -ldflags "-s -w" -o /relay .

FROM scratch

COPY --from=build /relay /relay

# Ohne Nutzer liefe der Dienst als root. 65532 ist die uebliche Kennung fuer
# "nobody" in Container-Basisimages und existiert hier gar nicht - genau richtig,
# denn dieser Prozess braucht keine Identitaet.
USER 65532:65532

EXPOSE 8080

ENTRYPOINT ["/relay"]

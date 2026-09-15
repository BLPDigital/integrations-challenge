# Arbeiten in diesem Repo

## Befehle, die der Mensch ausführen soll

Jeder Befehl, den ich hier jemandem zum Kopieren gebe, muss **von jedem
Verzeichnis aus** laufen. Nicht verhandelbar:

- kein stilles `cd irgendwohin` als Voraussetzung
- keine relativen Pfade wie `tools/foo.sh`
- keine Platzhalter wie `<dein Klon>` oder `<pfad>`
- der Befehl findet seinen Ort selbst, oder er nennt einen absoluten Pfad

Ein Befehl, der beim Benutzer mit `no such file or directory` endet, ist mein
Fehler. Und: erst hier ausprobieren, dann weitergeben.

Dieses Repo findet man von überall so:

```sh
REPO=$(find ~ -maxdepth 6 -type f -path '*/tools/make-candidate-repo.sh' 2>/dev/null | head -1 | sed 's|/tools/make-candidate-repo.sh$||')
```

## Sprache

Deutsch, ohne Fachjargon, ohne Gedankenstriche. Zahlen im Schweizer Format
(12'345). Englisch nur in Dateien des Repos, dort amerikanisches Englisch.

## Was hier was ist

- Dieses Repo ist das **interne**. Seine Historie enthält die Musterlösung, die
  versteckten Szenarien und das Bewertungsraster. Ein Kandidat darf es nie sehen.
- Entwicklungszweig: `claude/integrations-coding-challenge-1t2kxw`
- Das Repo für Kandidaten ist `BLPDigital/integrations-challenge`, ein einziger
  Commit ohne Historie, gebaut aus `tools/make-candidate-repo.sh`.
- `.claude/skills/challenge-review/` wertet eine Abgabe aus und baut daraus das
  Interview. Wird vom Paket für Kandidaten weggeschnitten.

## Fertige Befehle

Kandidaten-Repo auf den neuesten Stand bringen, von überall ausführbar:

```sh
REPO=$(find ~ -maxdepth 6 -type f -path '*/tools/make-candidate-repo.sh' 2>/dev/null | head -1 | sed 's|/tools/make-candidate-repo.sh$||') && test -n "$REPO" && cd "$REPO" && git fetch origin claude/integrations-coding-challenge-1t2kxw && git checkout claude/integrations-coding-challenge-1t2kxw && git pull && tools/make-candidate-repo.sh dist/candidate-repo && cd dist/candidate-repo && git remote add origin https://github.com/BLPDigital/integrations-challenge.git && git push --force origin main
```

Eine Abgabe bewerten, von überall ausführbar (Klon oder entpacktes Archiv als
erstes Argument):

```sh
REPO=$(find ~ -maxdepth 6 -type f -path '*/tools/make-candidate-repo.sh' 2>/dev/null | head -1 | sed 's|/tools/make-candidate-repo.sh$||') && cd "$REPO" && .claude/skills/challenge-review/scripts/collect-facts.sh /pfad/zur/abgabe
```

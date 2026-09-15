package deploy

import "strings"

func imageRepository(image string) string {
	repository := strings.SplitN(image, "@", 2)[0]
	if colon := strings.LastIndex(repository, ":"); colon > strings.LastIndex(repository, "/") {
		repository = repository[:colon]
	}
	return repository
}

// Remember images before a mutable tag (dev/latest) is moved by docker build.
// This also identifies legacy images which don't yet have an ownership label.
func imageSnapshot(t target) string {
	return "repository=" + quote(imageRepository(t.Image)) + "\n" +
		"before_images=$(docker image ls --no-trunc --format '{{.Repository}} {{.ID}}')\n" +
		"old_image_ids=$(printf '%s\\n' \"$before_images\" | while read -r repo id; do\n" +
		" [ \"$repo\" != \"$repository\" ] || printf '%s\\n' \"$id\"\ndone)\n"
}

// Cleanup only after readiness succeeds. No force or global prune: other
// containers, repositories and build caches must not be affected.
func cleanupImages(t target) string {
	return progress("Очистка старых образов "+imageRepository(t.Image)) + `
current_image=$(docker container inspect --format '{{.Image}}' ` + quote(t.Container) + `)
images=$(docker image ls --no-trunc --format '{{.Repository}} {{.Tag}} {{.ID}}')
printf '%s\n' "$images" | while read -r repo tag id; do
 [ "$repo" = "$repository" ] || continue
 [ "$id" != "$current_image" ] || continue
 [ "$tag" != '<none>' ] || continue
 users=$(docker container ls -aq --filter "ancestor=$id")
 if [ -n "$users" ]; then
  printf 'remote : Сохраняем %s:%s: образ используется контейнером\n' "$repo" "$tag"
  continue
 fi
 if docker image rm --no-prune -- "$repo:$tag" >/dev/null 2>&1; then
  printf 'remote : Удалён старый тег %s:%s\n' "$repo" "$tag"
 else
  printf 'remote : Не удалось удалить %s:%s; текущий сервис работает\n' "$repo" "$tag"
 fi
done
owned_dangling=$(docker image ls -q --no-trunc --filter dangling=true --filter ` + quote("label=deployctl.repository="+imageRepository(t.Image)) + `)
for id in $old_image_ids $owned_dangling; do
 [ "$id" != "$current_image" ] || continue
 # It may already have disappeared when its last tag was removed.
 tags=$(docker image inspect --format '{{len .RepoTags}}' "$id" 2>/dev/null) || continue
 [ "$tags" = 0 ] || continue
 users=$(docker container ls -aq --filter "ancestor=$id")
 if [ -n "$users" ]; then
  printf 'remote : Сохраняем %s: образ используется контейнером\n' "$id"
  continue
 fi
 if docker image rm --no-prune -- "$id" >/dev/null 2>&1; then
  printf 'remote : Удалён старый образ %s\n' "$id"
 else
  printf 'remote : Не удалось удалить %s; текущий сервис работает\n' "$id"
 fi
done
`
}

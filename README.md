gps_overlay_video
=================

Генерирует по GPX-треку оверлейное видео с мини-картой, предназначенное для последующего наложения на видеозаписи с экшен-камер. Карту берёт с OpenStreetMap.

Навайбкодено на коленке с помощью Gemini.

Пример запуска
--------------
```
go run main.go --bitrate 10M --border-color '#ffac33' -o /mnt/g/tmp/render/overlay1_go_v4_thunderforest.mp4 -dyn-map-scale -zoom-in-at-start -style thunderforest --widget-size 600 -2x -map-zoom 14 -gpx example.gpx
```

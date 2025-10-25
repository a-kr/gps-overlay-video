import argparse
import gpxpy
import gpxpy.gpx
import os
import requests
import math
from PIL import Image, ImageDraw, ImageFont
from io import BytesIO
import subprocess
from tqdm import tqdm
from haversine import haversine, Unit
import numpy as np

TILE_CACHE_DIR = "tiles"
TILE_URL = "https://tile.openstreetmap.org/{z}/{x}/{y}.png"
TILE_SIZE = 256

def deg2num(lat_deg, lon_deg, zoom):
    lat_rad = math.radians(lat_deg)
    n = 2.0 ** zoom
    xtile = int((lon_deg + 180.0) / 360.0 * n)
    ytile = int((1.0 - math.asinh(math.tan(lat_rad)) / math.pi) / 2.0 * n)
    return (xtile, ytile)

def num2deg(xtile, ytile, zoom):
    n = 2.0 ** zoom
    lon_deg = xtile / n * 360.0 - 180.0
    lat_rad = math.atan(math.sinh(math.pi * (1 - 2 * ytile / n)))
    lat_deg = math.degrees(lat_rad)
    return (lat_deg, lon_deg)

def get_pixel_coords(lat, lon, zoom):
    lat_rad = math.radians(lat)
    n = 2.0 ** zoom
    x = (lon + 180.0) / 360.0 * n
    y = (1.0 - math.asinh(math.tan(lat_rad)) / math.pi) / 2.0 * n
    return (x * TILE_SIZE, y * TILE_SIZE)

def download_tile(zoom, x, y):
    tile_path = os.path.join(TILE_CACHE_DIR, str(zoom), str(x), f"{y}.png")
    if os.path.exists(tile_path):
        return tile_path
    os.makedirs(os.path.dirname(tile_path), exist_ok=True)
    url = TILE_URL.format(z=zoom, x=x, y=y)
    try:
        response = requests.get(url, headers={'User-Agent': 'GpsOverlayVideo/0.1'})
        response.raise_for_status()
        with open(tile_path, 'wb') as f:
            f.write(response.content)
        return tile_path
    except requests.exceptions.RequestException as e:
        print(f"Error downloading tile {zoom}/{x}/{y}: {e}")
        return None

def get_tile_image(zoom, x, y):
    tile_path = download_tile(zoom, x, y)
    if tile_path:
        return Image.open(tile_path).convert("RGBA")
    return Image.new('RGBA', (TILE_SIZE, TILE_SIZE), (255, 255, 255, 0))

def parse_gpx(file_path):
    with open(file_path, 'r') as gpx_file:
        gpx = gpxpy.parse(gpx_file)
    points = []
    for track in gpx.tracks:
        for segment in track.segments:
            for point in segment.points:
                if point.elevation is None:
                    point.elevation = 0
                points.append({
                    'lat': point.latitude,
                    'lon': point.longitude,
                    'ele': point.elevation,
                    'time': point.time
                })
    # Simple interpolation
    all_points = []
    for i in range(len(points) - 1):
        p1 = points[i]
        p2 = points[i+1]
        all_points.append(p1)
        time_diff = (p2['time'] - p1['time']).total_seconds()
        if time_diff > 1:
            num_inter_points = int(time_diff) -1
            for j in range(num_inter_points):
                ratio = (j + 1) / (num_inter_points + 1)
                inter_lat = p1['lat'] + (p2['lat'] - p1['lat']) * ratio
                inter_lon = p1['lon'] + (p2['lon'] - p1['lon']) * ratio
                inter_ele = p1['ele'] + (p2['ele'] - p1['ele']) * ratio
                inter_time = p1['time'] + (p2['time'] - p1['time']) * ratio
                all_points.append({'lat': inter_lat, 'lon': inter_lon, 'ele': inter_ele, 'time': inter_time})

    all_points.append(points[-1])
    return all_points

def main():
    parser = argparse.ArgumentParser(description="Generate a transparent video overlay from a GPX file.")
    parser.add_argument("gpx_file", help="Path to the GPX file.")
    parser.add_argument("-o", "--output", help="Output video file name.", default="output.mp4")
    parser.add_argument("--video-size", help="Video dimensions (e.g., 1920x1080).", default="1920x1080")
    parser.add_argument("--bitrate", help="Video bitrate (e.g., 5M).", default="5M")
    parser.add_argument("--map-zoom", help="Map zoom level.", type=int, default=17)
    parser.add_argument("--widget-size", help="Map widget diameter in pixels.", type=int, default=400)
    parser.add_argument("--path-width", help="Width of the drawn path.", type=int, default=5)
    parser.add_argument("--path-color", help="Color of the drawn path.", default="red")


    args = parser.parse_args()

    video_width, video_height = map(int, args.video_size.split('x'))
    widget_diameter = args.widget_size
    widget_radius = widget_diameter // 2

    track_points = parse_gpx(args.gpx_file)
    if not track_points:
        print("No track points found.")
        return

    FRAME_RATE = 30
    ffmpeg_command = [
        'ffmpeg', '-y', '-f', 'image2pipe', '-vcodec', 'png', '-r', str(FRAME_RATE),
        '-i', '-', '-c:v', 'libx264', '-b:v', args.bitrate,
        '-pix_fmt', 'yuva420p', '-r', str(FRAME_RATE), args.output
    ]
    proc = subprocess.Popen(ffmpeg_command, stdin=subprocess.PIPE)

    total_distance = 0
    path_pixels = []
    
    try:
        font = ImageFont.truetype("sans-serif.ttf", 24)
    except IOError:
        font = ImageFont.load_default()

    print("Prefetching map tiles...")
    tile_coords = set()
    for point in tqdm(track_points):
        x, y = deg2num(point['lat'], point['lon'], args.map_zoom)
        tile_coords.add((x, y))
        # Prefetch surrounding tiles as well
        for dx in [-1, 0, 1]:
            for dy in [-1, 0, 1]:
                tile_coords.add((x + dx, y + dy))

    for x, y in tqdm(list(tile_coords)):
        download_tile(args.map_zoom, x, y)


    print("Generating video frames...")
    for i in tqdm(range(len(track_points))):
        current_point = track_points[i]
        
        # --- Calculations ---
        if i > 0:
            prev_point = track_points[i-1]
            distance_delta = haversine((prev_point['lat'], prev_point['lon']), (current_point['lat'], current_point['lon']), unit=Unit.KILOMETERS)
            total_distance += distance_delta
            time_delta = (current_point['time'] - prev_point['time']).total_seconds()
            speed = (distance_delta * 3600) / time_delta if time_delta > 0 else 0
            ele_delta = current_point['ele'] - prev_point['ele']
            dist_m = distance_delta * 1000
            slope = (ele_delta / dist_m) * 100 if dist_m > 0 else 0
        else:
            speed, slope = 0, 0

        # --- Map Rendering ---
        center_x, center_y = deg2num(current_point['lat'], current_point['lon'], args.map_zoom)
        
        # Create a large enough image to stitch tiles
        map_image = Image.new('RGBA', (TILE_SIZE * 3, TILE_SIZE * 3))
        for dx in [-1, 0, 1]:
            for dy in [-1, 0, 1]:
                tile = get_tile_image(args.map_zoom, center_x + dx, center_y + dy)
                map_image.paste(tile, ((dx + 1) * TILE_SIZE, (dy + 1) * TILE_SIZE))

        # Position of the current point on the stitched map
        px, py = get_pixel_coords(current_point['lat'], current_point['lon'], args.map_zoom)
        center_px_on_map = (px % TILE_SIZE) + TILE_SIZE
        center_py_on_map = (py % TILE_SIZE) + TILE_SIZE
        
        # --- Path Drawing ---
        # Convert all historical points to pixels on the current map canvas
        path_pixels_on_map = []
        for p in track_points[:i+1]:
            ppx, ppy = get_pixel_coords(p['lat'], p['lon'], args.map_zoom)
            # Relative to the top-left of the 3x3 stitched map
            path_pixels_on_map.append((
                ppx - (center_x - 1) * TILE_SIZE,
                ppy - (center_y - 1) * TILE_SIZE
            ))

        map_draw = ImageDraw.Draw(map_image)
        if len(path_pixels_on_map) > 1:
            map_draw.line(path_pixels_on_map, fill=args.path_color, width=args.path_width)

        # Current position marker
        map_draw.ellipse(
            (center_px_on_map - 10, center_py_on_map - 10, center_px_on_map + 10, center_py_on_map + 10),
            fill='blue', outline='white', width=2
        )

        # Crop circular widget
        left = center_px_on_map - widget_radius
        top = center_py_on_map - widget_radius
        right = center_px_on_map + widget_radius
        bottom = center_py_on_map + widget_radius
        
        map_widget = map_image.crop((left, top, right, bottom))
        
        mask = Image.new('L', (widget_diameter, widget_diameter), 0)
        mask_draw = ImageDraw.Draw(mask)
        mask_draw.ellipse((0, 0, widget_diameter, widget_diameter), fill=255)
        map_widget.putalpha(mask)

        # --- Final Frame Composition ---
        frame = Image.new('RGBA', (video_width, video_height), (0, 0, 0, 0))
        
        # Place map widget at the bottom-right corner
        map_pos_x = video_width - widget_diameter - 20
        map_pos_y = video_height - widget_diameter - 20
        frame.paste(map_widget, (map_pos_x, map_pos_y), map_widget)

        # --- Text Indicators ---
        text_draw = ImageDraw.Draw(frame)
        text_y = map_pos_y - 100
        text_draw.text((20, text_y), f"Distance: {total_distance:.2f} km", font=font, fill="white")
        text_draw.text((20, text_y + 30), f"Speed: {speed:.1f} km/h", font=font, fill="white")
        text_draw.text((20, text_y + 60), f"Slope: {slope:.1f} %", font=font, fill="white")

        frame.save(proc.stdin, 'PNG')

    proc.stdin.close()
    proc.wait()

    print(f"Video saved to {args.output}")

if __name__ == "__main__":
    main()

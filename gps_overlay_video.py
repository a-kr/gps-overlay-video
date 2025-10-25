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
from datetime import timedelta

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
    return points

def find_point_for_time(time_offset, start_time, points):
    target_time = start_time + timedelta(seconds=time_offset)
    for i in range(len(points) - 1):
        p1 = points[i]
        p2 = points[i+1]
        if p1['time'] <= target_time <= p2['time']:
            time_diff = (p2['time'] - p1['time']).total_seconds()
            if time_diff == 0:
                return p1
            ratio = (target_time - p1['time']).total_seconds() / time_diff
            inter_lat = p1['lat'] + (p2['lat'] - p1['lat']) * ratio
            inter_lon = p1['lon'] + (p2['lon'] - p1['lon']) * ratio
            inter_ele = p1['ele'] + (p2['ele'] - p1['ele']) * ratio
            return {'lat': inter_lat, 'lon': inter_lon, 'ele': inter_ele, 'time': target_time}
    return points[-1]

def main():
    parser = argparse.ArgumentParser(description="Generate a transparent video overlay from a GPX file.")
    parser.add_argument("gpx_file", help="Path to the GPX file.")
    parser.add_argument("-o", "--output", help="Output video file name.", default="output.mp4")
    parser.add_argument("--video-size", help="Video dimensions (e.g., 1920x1080).", default="640x480")
    parser.add_argument("--bitrate", help="Video bitrate (e.g., 5M).", default="5M")
    parser.add_argument("--map-zoom", help="Map zoom level. Default 15 is approx 1km diameter for a 400px widget.", type=int, default=15)
    parser.add_argument("--widget-size", help="Map widget diameter in pixels.", type=int, default=300)
    parser.add_argument("--path-width", help="Width of the drawn path.", type=int, default=4)
    parser.add_argument("--path-color", help="Color of the drawn path.", default="red")
    parser.add_argument("--border-color", help="Color of the map border.", default="white")

    args = parser.parse_args()

    video_width, video_height = map(int, args.video_size.split('x'))
    widget_diameter = args.widget_size
    widget_radius = widget_diameter // 2

    track_points = parse_gpx(args.gpx_file)
    if not track_points:
        print("No track points found.")
        return

    FRAME_RATE = 30
    start_time = track_points[0]['time']
    total_duration = (track_points[-1]['time'] - start_time).total_seconds()
    total_frames = int(total_duration * FRAME_RATE)

    ffmpeg_command = [
        'ffmpeg', '-y', '-f', 'image2pipe', '-vcodec', 'png', '-r', str(FRAME_RATE),
        '-i', '-', '-c:v', 'libx264', '-b:v', args.bitrate,
        '-pix_fmt', 'yuva420p', '-r', str(FRAME_RATE), args.output
    ]
    proc = subprocess.Popen(ffmpeg_command, stdin=subprocess.PIPE)

    total_distance = 0
    
    try:
        font = ImageFont.truetype("sans-serif.ttf", 32)
    except IOError:
        font = ImageFont.load_default()

    print("Prefetching map tiles...")
    tile_coords = set()
    for point in tqdm(track_points):
        x, y = deg2num(point['lat'], point['lon'], args.map_zoom)
        for dx in [-1, 0, 1]:
            for dy in [-1, 0, 1]:
                tile_coords.add((x + dx, y + dy))
    for x, y in tqdm(list(tile_coords)):
        download_tile(args.map_zoom, x, y)

    print("Generating video frames...")
    for frame_num in tqdm(range(total_frames)):
        time_offset = frame_num / FRAME_RATE
        current_point = find_point_for_time(time_offset, start_time, track_points)
        
        # --- Calculations ---
        if frame_num > 0:
            prev_time_offset = (frame_num - 1) / FRAME_RATE
            prev_point = find_point_for_time(prev_time_offset, start_time, track_points)
            distance_delta = haversine((prev_point['lat'], prev_point['lon']), (current_point['lat'], current_point['lon']), unit=Unit.KILOMETERS)
            total_distance += distance_delta
            speed = distance_delta * FRAME_RATE * 3600
            ele_delta = current_point['ele'] - prev_point['ele']
            dist_m = distance_delta * 1000
            slope = (ele_delta / dist_m) * 100 if dist_m > 0 else 0
        else:
            speed, slope = 0, 0

        # --- Map Rendering ---
        center_x, center_y = deg2num(current_point['lat'], current_point['lon'], args.map_zoom)
        map_image = Image.new('RGBA', (TILE_SIZE * 3, TILE_SIZE * 3))
        for dx in [-1, 0, 1]:
            for dy in [-1, 0, 1]:
                tile = get_tile_image(args.map_zoom, center_x + dx, center_y + dy)
                map_image.paste(tile, ((dx + 1) * TILE_SIZE, (dy + 1) * TILE_SIZE))

        px, py = get_pixel_coords(current_point['lat'], current_point['lon'], args.map_zoom)
        center_px_on_map = (px % TILE_SIZE) + TILE_SIZE
        center_py_on_map = (py % TILE_SIZE) + TILE_SIZE
        
        # --- Path Drawing ---
        path_pixels_on_map = []
        current_path_points = [p for p in track_points if p['time'] <= current_point['time']]
        for p in current_path_points:
            ppx, ppy = get_pixel_coords(p['lat'], p['lon'], args.map_zoom)
            path_pixels_on_map.append((ppx - (center_x - 1) * TILE_SIZE, ppy - (center_y - 1) * TILE_SIZE))

        map_draw = ImageDraw.Draw(map_image)
        if len(path_pixels_on_map) > 1:
            map_draw.line(path_pixels_on_map, fill=args.path_color, width=args.path_width)

        map_draw.ellipse((center_px_on_map - 8, center_py_on_map - 8, center_px_on_map + 8, center_py_on_map + 8), fill='blue', outline='white', width=2)

        left, top = center_px_on_map - widget_radius, center_py_on_map - widget_radius
        map_widget = map_image.crop((left, top, left + widget_diameter, top + widget_diameter))
        
        mask = Image.new('L', (widget_diameter, widget_diameter), 0)
        mask_draw = ImageDraw.Draw(mask)
        mask_draw.ellipse((0, 0, widget_diameter, widget_diameter), fill=255)
        map_widget.putalpha(mask)

        # --- Final Frame Composition ---
        frame = Image.new('RGBA', (video_width, video_height), (0, 0, 0, 0))
        frame_draw = ImageDraw.Draw(frame)
        
        map_pos_x = video_width - widget_diameter - 20
        map_pos_y = video_height - widget_diameter - 120 # Move up
        frame.paste(map_widget, (map_pos_x, map_pos_y), map_widget)

        # Border
        frame_draw.ellipse((map_pos_x, map_pos_y, map_pos_x + widget_diameter, map_pos_y + widget_diameter), outline=args.border_color, width=3)

        # --- Text Indicators ---
        text_y = map_pos_y + widget_diameter + 15
        frame_draw.text((map_pos_x, text_y), f"Dist: {total_distance:.2f} km", font=font, fill="white")
        frame_draw.text((map_pos_x, text_y + 40), f"Speed: {speed:.1f} km/h", font=font, fill="white")
        frame_draw.text((map_pos_x, text_y + 80), f"Slope: {slope:.1f} %", font=font, fill="white")

        frame.save(proc.stdin, 'PNG')

    proc.stdin.close()
    proc.wait()

    print(f"Video saved to {args.output}")

if __name__ == "__main__":
    main()
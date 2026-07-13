import { useEffect, useRef } from "react";
import uPlot from "uplot";
import type { ExperimentRegion } from "../experiments";

export interface ChartProps {
  /** Aligned [x(seconds), y] arrays. */
  data: uPlot.AlignedData;
  color: string;
  height: number;
  /** Visible x range in epoch seconds. */
  xRange: [number, number];
  /** Optional fixed y range (boolean strips, or a per-chart Y-lock). */
  yRange?: [number, number];
  /** Render as a stepped line (boolean channels). */
  stepped?: boolean;
  /** Shared cursor sync key so all charts share a time cursor. */
  syncKey: string;
  /** Experiment time bands to shade behind the series. */
  regions?: ExperimentRegion[];
  /** Wheel zoom about a cursor position: factor>1 widens, <1 narrows. */
  onZoomAt?: (centerSec: number, factor: number) => void;
  /** Drag-box zoom to an explicit [min,max] (epoch seconds). */
  onZoomRange?: (minSec: number, maxSec: number) => void;
  /** Shift/middle-drag horizontal pan, seconds (positive = later). */
  onPan?: (deltaSec: number) => void;
  /** Double-click: reset to the live window. */
  onReset?: () => void;
}

const AXIS_COLOR = "#7d8892";
const GRID_COLOR = "rgba(120,134,148,0.12)";
const REGION_FILL = "rgba(79,209,197,0.07)";
const REGION_FILL_RUNNING = "rgba(229,72,77,0.08)";
const REGION_EDGE = "rgba(79,209,197,0.45)";
const REGION_EDGE_RUNNING = "rgba(229,72,77,0.5)";
const REGION_LABEL = "rgba(199,208,217,0.75)";

/** Wheel notch zoom step. deltaY>0 (scroll down) widens (zooms out). */
const WHEEL_FACTOR = 1.2;
/** Min drag-box width (CSS px) to treat as a zoom rather than a stray click. */
const MIN_DRAG_PX = 6;

function axis(): uPlot.Axis {
  return {
    stroke: AXIS_COLOR,
    grid: { stroke: GRID_COLOR, width: 1 },
    ticks: { stroke: GRID_COLOR, width: 1 },
    font: '11px "JetBrains Mono", ui-monospace, monospace',
  };
}

/** Latest interaction callbacks + regions, read by the imperative handlers. */
interface Live {
  onZoomAt?: ChartProps["onZoomAt"];
  onZoomRange?: ChartProps["onZoomRange"];
  onPan?: ChartProps["onPan"];
  onReset?: ChartProps["onReset"];
  regions: ExperimentRegion[];
}

/**
 * The experiment-overlay plugin: shades each experiment's time band behind the
 * series (via `drawClear`, before the line) and labels it at the top edge (via
 * `draw`, on top). Pulls regions from a mutable ref so a single plugin instance
 * tracks live updates without re-instantiating the plot. A running experiment's
 * band already extends to `now` (see experimentRegions()).
 */
function regionPlugin(live: React.MutableRefObject<Live>): uPlot.Plugin {
  const bandFill = (u: uPlot) => {
    const regions = live.current.regions;
    if (regions.length === 0) return;
    const { ctx } = u;
    const { left, top, width, height } = u.bbox;
    ctx.save();
    ctx.beginPath();
    ctx.rect(left, top, width, height);
    ctx.clip();
    for (const r of regions) {
      const x0 = u.valToPos(r.startSec, "x", true);
      const x1 = u.valToPos(r.endSec, "x", true);
      if (x1 < left || x0 > left + width) continue;
      const cx0 = Math.max(x0, left);
      const cx1 = Math.min(x1, left + width);
      ctx.fillStyle = r.running ? REGION_FILL_RUNNING : REGION_FILL;
      ctx.fillRect(cx0, top, Math.max(0, cx1 - cx0), height);
      // Faint start/end edges (only when the edge is actually in view).
      ctx.strokeStyle = r.running ? REGION_EDGE_RUNNING : REGION_EDGE;
      ctx.lineWidth = 1;
      if (x0 >= left && x0 <= left + width) {
        ctx.beginPath();
        ctx.moveTo(x0, top);
        ctx.lineTo(x0, top + height);
        ctx.stroke();
      }
      if (!r.running && x1 >= left && x1 <= left + width) {
        ctx.beginPath();
        ctx.moveTo(x1, top);
        ctx.lineTo(x1, top + height);
        ctx.stroke();
      }
    }
    ctx.restore();
  };

  const labels = (u: uPlot) => {
    const regions = live.current.regions;
    if (regions.length === 0) return;
    const { ctx } = u;
    const { left, top, width } = u.bbox;
    ctx.save();
    ctx.beginPath();
    ctx.rect(left, top, width, u.bbox.height);
    ctx.clip();
    ctx.font = `${Math.round(10 * uPlot.pxRatio)}px "JetBrains Mono", ui-monospace, monospace`;
    ctx.textBaseline = "top";
    ctx.fillStyle = REGION_LABEL;
    for (const r of regions) {
      const x0 = u.valToPos(r.startSec, "x", true);
      const x1 = u.valToPos(r.endSec, "x", true);
      if (x1 < left || x0 > left + width) continue;
      const lx = Math.max(x0, left) + 4 * uPlot.pxRatio;
      if (lx > left + width - 8 * uPlot.pxRatio) continue; // no room for text
      ctx.fillText(r.name, lx, top + 3 * uPlot.pxRatio);
    }
    ctx.restore();
  };

  return { hooks: { drawClear: bandFill, draw: labels } };
}

/**
 * Thin uPlot wrapper. Creates the canvas once and updates data + x-scale
 * imperatively (never re-instantiates on data change). setScale is coalesced
 * into a rAF so a burst of wheel/drag events triggers at most one redraw per
 * frame — keeping 60fps with many charts. Gesture handlers translate pointer
 * input into pure window ops on the shared zoom state (see zoom.ts); this
 * component holds no window math itself.
 */
export function Chart({
  data,
  color,
  height,
  xRange,
  yRange,
  stepped,
  syncKey,
  regions,
  onZoomAt,
  onZoomRange,
  onPan,
  onReset,
}: ChartProps) {
  const hostRef = useRef<HTMLDivElement>(null);
  const plotRef = useRef<uPlot | null>(null);

  // Latest callbacks/regions for the imperative listeners (avoids re-binding).
  const liveRef = useRef<Live>({ regions: regions ?? [] });
  liveRef.current = {
    onZoomAt,
    onZoomRange,
    onPan,
    onReset,
    regions: regions ?? [],
  };

  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;

    const steppedPaths =
      stepped && uPlot.paths?.stepped ? uPlot.paths.stepped({ align: 1 }) : undefined;

    const opts: uPlot.Options = {
      width: host.clientWidth || 600,
      height,
      cursor: {
        sync: { key: syncKey },
        // Show the x drag-box, but DON'T let uPlot apply the zoom itself — we
        // route it through the shared zoom state so every chart stays synced.
        drag: { x: true, y: false, setScale: false },
        points: { size: 5 },
      },
      legend: { show: false },
      scales: {
        x: { time: true },
        y: yRange ? { range: yRange } : { auto: true },
      },
      axes: [axis(), { ...axis(), size: 52 }],
      series: [
        {},
        {
          stroke: color,
          width: 1.5,
          points: { show: false },
          ...(steppedPaths ? { paths: steppedPaths } : {}),
        },
      ],
      hooks: {
        // Drag-box release: convert the pixel selection to an epoch-second range
        // and hand it up; then clear the selection so it doesn't linger.
        setSelect: [
          (u) => {
            const sel = u.select;
            if (sel.width < MIN_DRAG_PX) return;
            const min = u.posToVal(sel.left, "x");
            const max = u.posToVal(sel.left + sel.width, "x");
            liveRef.current.onZoomRange?.(min, max);
            u.setSelect({ left: 0, top: 0, width: 0, height: 0 }, false);
          },
        ],
      },
      plugins: [regionPlugin(liveRef)],
    };

    const u = new uPlot(opts, data, host);
    plotRef.current = u;

    const over = u.over;

    // Wheel zoom about the cursor (x only). passive:false to preventDefault the
    // page scroll.
    const onWheel = (e: WheelEvent) => {
      if (!liveRef.current.onZoomAt) return;
      e.preventDefault();
      const rect = over.getBoundingClientRect();
      const centerSec = u.posToVal(e.clientX - rect.left, "x");
      const factor = e.deltaY > 0 ? WHEEL_FACTOR : 1 / WHEEL_FACTOR;
      liveRef.current.onZoomAt(centerSec, factor);
    };

    // Shift-drag or middle-drag = horizontal pan. We handle it before uPlot's
    // own selection (capture + stopImmediatePropagation) so the drag-box zoom
    // and the pan gesture never fight.
    let panning = false;
    let lastLeft = 0;
    const onDown = (e: MouseEvent) => {
      if (!liveRef.current.onPan) return;
      const isPan = e.button === 1 || (e.button === 0 && e.shiftKey);
      if (!isPan) return;
      e.preventDefault();
      e.stopImmediatePropagation();
      panning = true;
      const rect = over.getBoundingClientRect();
      lastLeft = e.clientX - rect.left;
      window.addEventListener("mousemove", onMove, true);
      window.addEventListener("mouseup", onUp, true);
    };
    const onMove = (e: MouseEvent) => {
      if (!panning) return;
      const rect = over.getBoundingClientRect();
      const left = e.clientX - rect.left;
      // Δ seconds between the last and current cursor position. Dragging right
      // (left increases) reveals earlier data, so the shift is negative.
      const deltaSec = u.posToVal(lastLeft, "x") - u.posToVal(left, "x");
      lastLeft = left;
      if (deltaSec !== 0) liveRef.current.onPan?.(deltaSec);
    };
    const onUp = () => {
      panning = false;
      window.removeEventListener("mousemove", onMove, true);
      window.removeEventListener("mouseup", onUp, true);
    };
    const onDblClick = () => liveRef.current.onReset?.();

    over.addEventListener("wheel", onWheel, { passive: false });
    over.addEventListener("mousedown", onDown, true);
    over.addEventListener("dblclick", onDblClick);

    const ro = new ResizeObserver(() => {
      u.setSize({ width: host.clientWidth || 600, height });
    });
    ro.observe(host);

    return () => {
      over.removeEventListener("wheel", onWheel);
      over.removeEventListener("mousedown", onDown, true);
      over.removeEventListener("dblclick", onDblClick);
      window.removeEventListener("mousemove", onMove, true);
      window.removeEventListener("mouseup", onUp, true);
      ro.disconnect();
      u.destroy();
      plotRef.current = null;
    };
    // Recreate only when structural options change, not on data ticks.
  }, [height, color, stepped, syncKey, yRange]);

  // Data + x-scale updates, coalesced into one rAF per frame so a burst of
  // zoom/pan/tick updates costs at most one redraw.
  const pendingRef = useRef<{ data: uPlot.AlignedData; xRange: [number, number] } | null>(null);
  const rafRef = useRef<number | null>(null);
  useEffect(() => {
    pendingRef.current = { data, xRange };
    if (rafRef.current !== null) return;
    rafRef.current = requestAnimationFrame(() => {
      rafRef.current = null;
      const u = plotRef.current;
      const p = pendingRef.current;
      if (!u || !p) return;
      u.setData(p.data, false);
      u.setScale("x", { min: p.xRange[0], max: p.xRange[1] });
    });
  }, [data, xRange]);

  useEffect(() => {
    return () => {
      if (rafRef.current !== null) cancelAnimationFrame(rafRef.current);
    };
  }, []);

  return <div className="chart-canvas" ref={hostRef} />;
}

import { useEffect, useRef } from "react";
import uPlot from "uplot";

export interface ChartProps {
  /** Aligned [x(seconds), y] arrays. */
  data: uPlot.AlignedData;
  color: string;
  height: number;
  /** Visible x range in epoch seconds. */
  xRange: [number, number];
  /** Optional fixed y range (used for boolean state strips: [-0.1, 1.1]). */
  yRange?: [number, number];
  /** Render as a stepped line (boolean channels). */
  stepped?: boolean;
  /** Shared cursor sync key so all charts share a time cursor. */
  syncKey: string;
}

const AXIS_COLOR = "#7d8892";
const GRID_COLOR = "rgba(120,134,148,0.12)";

function axis(): uPlot.Axis {
  return {
    stroke: AXIS_COLOR,
    grid: { stroke: GRID_COLOR, width: 1 },
    ticks: { stroke: GRID_COLOR, width: 1 },
    font: '11px "JetBrains Mono", ui-monospace, monospace',
  };
}

/**
 * Thin uPlot wrapper: creates the canvas once and updates data + x-scale
 * imperatively each tick (never re-instantiates on data change).
 */
export function Chart({ data, color, height, xRange, yRange, stepped, syncKey }: ChartProps) {
  const hostRef = useRef<HTMLDivElement>(null);
  const plotRef = useRef<uPlot | null>(null);

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
        drag: { x: false, y: false },
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
    };

    const u = new uPlot(opts, data, host);
    plotRef.current = u;

    const ro = new ResizeObserver(() => {
      u.setSize({ width: host.clientWidth || 600, height });
    });
    ro.observe(host);

    return () => {
      ro.disconnect();
      u.destroy();
      plotRef.current = null;
    };
    // Recreate only when structural options change, not on data ticks.
  }, [height, color, stepped, syncKey, yRange]);

  useEffect(() => {
    const u = plotRef.current;
    if (!u) return;
    u.setData(data, false);
    u.setScale("x", { min: xRange[0], max: xRange[1] });
  }, [data, xRange]);

  return <div className="chart-canvas" ref={hostRef} />;
}

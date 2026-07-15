import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import "uplot/dist/uPlot.min.css";
import "react-grid-layout/css/styles.css";
import "./styles.css";

const root = document.getElementById("root");
if (!root) throw new Error("missing #root element");

createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
);

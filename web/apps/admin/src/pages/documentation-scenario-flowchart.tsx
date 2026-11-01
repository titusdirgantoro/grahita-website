import { useState } from "react";

import styles from "./documentation.module.css";

type ScenarioFlowProps = {
  id: string;
  action: string;
  expected: string;
};

type Box = { lines: string[]; height: number; y: number };

function clean(value: string) {
  return value.replaceAll("`", "").replace(/\s+/g, " ").trim().replace(/[.;]\s*$/, "");
}

function stepsFor(action: string, expected: string) {
  const parts = action.split(/;\s*|(?<=[.!?])\s+(?=[A-Z])/u).map(clean).filter(Boolean);
  if (!expected && parts.length === 1 && parts[0].includes(":")) {
    const [instruction, outcome] = parts[0].split(/:\s*(.*)/s).filter(Boolean);
    if (instruction && outcome) return { steps: [instruction], result: outcome };
  }
  const result = expected ? clean(expected) : parts.length > 1 ? parts.pop()! : clean(action);
  return { steps: parts.length ? parts : [clean(action)], result };
}

function wrap(value: string, limit: number) {
  const words = value.split(/\s+/);
  const lines: string[] = [];
  let line = "";
  for (const word of words) {
    if (word.length > limit) {
      if (line) lines.push(line);
      for (let index = 0; index < word.length; index += limit) {
        const chunk = word.slice(index, index + limit);
        if (chunk.length === limit) lines.push(chunk);
        else line = chunk;
      }
      if (word.length % limit === 0) line = "";
      continue;
    }
    if (line && `${line} ${word}`.length > limit) {
      lines.push(line);
      line = word;
    } else {
      line = line ? `${line} ${word}` : word;
    }
  }
  if (line) lines.push(line);
  return lines;
}

function FlowText({ x, y, lines }: { x: number; y: number; lines: string[] }) {
  const firstY = y - ((lines.length - 1) * 18) / 2 + 5;
  return <text className={styles.flowShapeText} textAnchor="middle">
    {lines.map((line, index) => <tspan key={`${index}-${line}`} x={x} y={firstY + index * 18}>{line}</tspan>)}
  </text>;
}

function ScenarioFlow({ id, action, expected }: ScenarioFlowProps) {
  const { steps, result } = stepsFor(action, expected);
  let nextTop = 112;
  const boxes: Box[] = steps.map((step) => {
    const lines = wrap(step, 56);
    const height = Math.max(76, lines.length * 18 + 30);
    const box = { lines, height, y: nextTop + height / 2 };
    nextTop += height + 38;
    return box;
  });
  const decisionY = nextTop + 44;
  const outcomeTop = decisionY + 135;
  const successLines = wrap(result, 35);
  const successHeight = Math.max(90, successLines.length * 18 + 32);
  const failureLines = wrap("Catat perbedaan hasil, bukti, dan langkah reproduksi", 35);
  const failureHeight = Math.max(90, failureLines.length * 18 + 32);
  const terminalY = outcomeTop + Math.max(successHeight, failureHeight) + 65;
  const height = terminalY + 48;
  const arrowId = `scenario-arrow-${id.toLowerCase()}`;
  const marker = `url(#${arrowId})`;

  return <figure className={styles.sectionFlow} aria-label={`Diagram alur skenario ${id}`}>
    <figcaption>Diagram alur · {id}</figcaption>
    <p className={styles.flowLegend}>Ikuti langkah dari atas. Cabang “Sesuai” menunjukkan hasil yang diharapkan; cabang “Berbeda” menunjukkan cara mencatat temuan. Diagram ini bukan hasil pengujian.</p>
    <p className={styles.flowScrollHint}>Geser diagram ke kanan untuk melihat kedua cabang hasil →</p>
    <div className={styles.flowChartViewport} tabIndex={0} aria-label={`Diagram skenario ${id}. Geser secara horizontal pada layar kecil untuk melihat seluruh alur.`}>
      <svg className={styles.scenarioFlowSvg} width="840" height={height} viewBox={`0 0 840 ${height}`} role="img" aria-label={`${id}: mulai; ${steps.join("; ")}; apakah hasil sesuai; sesuai: ${result}; berbeda: catat temuan.`}>
        <defs><marker id={arrowId} viewBox="0 0 8 8" refX="7" refY="4" markerWidth="8" markerHeight="8" orient="auto"><path className={styles.flowArrowHead} d="M0 0 L8 4 L0 8 Z" /></marker></defs>
        <g className={styles.flowConnectors} fill="none" markerEnd={marker}>
          <path d="M420 69 V104" />
          {boxes.slice(1).map((box, index) => <path key={index} d={`M420 ${boxes[index].y + boxes[index].height / 2 + 4} V${box.y - box.height / 2 - 8}`} />)}
          <path d={`M420 ${boxes.at(-1)!.y + boxes.at(-1)!.height / 2 + 4} V${decisionY - 70}`} />
          <path d={`M330 ${decisionY} H205 V${outcomeTop - 8}`} />
          <path d={`M510 ${decisionY} H635 V${outcomeTop - 8}`} />
          <path d={`M205 ${outcomeTop + successHeight + 4} V${terminalY - 29}`} />
          <path d={`M635 ${outcomeTop + failureHeight + 4} V${terminalY - 29}`} />
        </g>
        <text className={styles.flowBranchLabel} x="260" y={decisionY - 12} textAnchor="middle">Sesuai</text>
        <text className={styles.flowBranchLabel} x="578" y={decisionY - 12} textAnchor="middle">Berbeda</text>
        <ellipse className={styles.flowTerminal} cx="420" cy="42" rx="68" ry="27" />
        <FlowText x={420} y={42} lines={["Mulai"]} />
        {boxes.map((box, index) => <g key={index}>
          <polygon className={styles.flowInput} points={`178,${box.y - box.height / 2} 680,${box.y - box.height / 2} 662,${box.y + box.height / 2} 160,${box.y + box.height / 2}`} />
          <FlowText x={420} y={box.y} lines={box.lines} />
        </g>)}
        <polygon className={styles.flowDecisionDiamond} points={`420,${decisionY - 70} 510,${decisionY} 420,${decisionY + 70} 330,${decisionY}`} />
        <FlowText x={420} y={decisionY} lines={["Hasil sesuai", "skenario?"]} />
        <rect className={styles.flowProcess} x="45" y={outcomeTop} width="320" height={successHeight} rx="8" />
        <FlowText x={205} y={outcomeTop + successHeight / 2} lines={successLines} />
        <rect className={styles.flowProcess} x="475" y={outcomeTop} width="320" height={failureHeight} rx="8" />
        <FlowText x={635} y={outcomeTop + failureHeight / 2} lines={failureLines} />
        <ellipse className={styles.flowTerminal} cx="205" cy={terminalY} rx="85" ry="27" />
        <FlowText x={205} y={terminalY} lines={["Sesuai"]} />
        <ellipse className={styles.flowTerminal} cx="635" cy={terminalY} rx="100" ry="27" />
        <FlowText x={635} y={terminalY} lines={["Catat temuan"]} />
      </svg>
    </div>
  </figure>;
}

export function ScenarioFlowToggle(props: ScenarioFlowProps) {
  const [open, setOpen] = useState(false);
  return <>
    <button className={styles.scenarioFlowToggle} type="button" aria-expanded={open} onClick={() => setOpen((current) => !current)}>
      {open ? "Sembunyikan" : "Lihat"} diagram alur {props.id}
    </button>
    {open ? <ScenarioFlow {...props} /> : null}
  </>;
}

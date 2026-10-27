import type {EChartsCoreOption} from 'echarts/core';
import {quotaFlow, scopeKey, configuredLimit, isManaged, assignedBytes, type QuotaNode, type QuotaPolicy} from './quota-policy.ts';
import {storageLabel} from './quota-preview.ts';
import {storageScopeLabel} from './storage-labels.ts';

type Colors = {text: string; muted: string; surface: string; line: string};
export function quotaTreeOption(nodes: QuotaNode[], policy: QuotaPolicy, selected: string[], width: number, colors: Colors, consumption = false): EChartsCoreOption {
  const ids = new Set(nodes.map(n => n.id)), flow = quotaFlow(nodes, consumption ? {...policy, managed: false} : policy);
  const total = flow.get(scopeKey([])) ?? 0, narrow = width < 700;
  const branchLabelWidth = narrow ? 100 : 124;
  const columnWidth = Math.max(16, (width - branchLabelWidth - 24) / 4 - 20);
  return {
    tooltip: {
      trigger: 'item', renderMode: 'richText', confine: true,
      backgroundColor: colors.surface, borderColor: colors.line, textStyle: {color: colors.text},
      formatter: (p: {data: {scope: string[]}}) => {
        const path = p.data.scope, node = nodes.find(n => n.id === scopeKey(path)), limit = configuredLimit(policy, path);
        return `${storageScopeLabel(path)}\n${storageLabel(node?.bytes ?? 0)} Consumed\n${!isManaged(policy, path) ? 'Unmanaged · Inherited Limit' : limit === undefined ? 'Inherited Limit' : `${storageLabel(limit)} ${path.length === 4 ? 'Quota' : 'Pool Limit'}`}\n${storageLabel(assignedBytes(policy, path))} Assigned`;
      },
    },
    series: [{
      type: 'sankey', orient: 'horizontal', left: 12, right: branchLabelWidth + 24,
      top: 10, bottom: 10, nodeAlign: 'left', nodeWidth: 12, nodeGap: 0,
      layoutIterations: 0, draggable: false,
      // Only branch labels participate in vertical collision resolution. Flow
      // geometry remains proportional; guide lines connect displaced labels.
      labelLayout: (p: {dataIndex?: number; dataType?: string; labelRect: {x: number; y: number; height: number}}) =>
        p.dataType !== 'edge' && p.dataIndex !== undefined && nodes[p.dataIndex]?.kind === 'branch'
          ? {x: p.labelRect.x, y: p.labelRect.y + p.labelRect.height / 2, moveOverlap: 'shiftY', hideOverlap: false} : {},
      data: nodes.map(n => {
        const branch = n.kind === 'branch';
        return {
          name: n.id, value: flow.get(n.id) ?? 0, scope: n.path, depth: n.path.length,
          itemStyle: {borderColor: colors.surface, borderWidth: 1, color: n.id === scopeKey(selected) ? colors.text : isManaged(policy, n.path) ? '#7c8d87' : colors.muted, opacity: isManaged(policy, n.path) ? 1 : .4},
          label: {
            show: branch || total > 0 && (flow.get(n.id) ?? 0) / total * 620 >= (narrow ? 17 : 36),
            formatter: branch || narrow ? n.name : `${n.name}\n${!isManaged(policy, n.path) ? 'Unmanaged' : configuredLimit(policy, n.path) === undefined ? 'Inherited' : storageLabel(configuredLimit(policy, n.path)!)}`,
            position: 'right', width: branch ? branchLabelWidth : columnWidth,
            overflow: branch ? undefined : 'truncate', distance: branch ? 18 : 5,
            color: colors.text, fontSize: narrow ? 10 : 12, lineHeight: 17,
          },
          labelLine: {show: branch, length2: 5, lineStyle: {color: colors.muted, width: .7}},
        };
      }),
      links: nodes.filter(n => n.path.length && ids.has(scopeKey(n.path.slice(0, -1))))
        .map(n => ({source: scopeKey(n.path.slice(0, -1)), target: n.id, scope: n.path, value: flow.get(n.id) ?? 0})),
      lineStyle: {color: 'gradient', opacity: .25, curveness: .5},
      emphasis: {focus: 'adjacency', lineStyle: {opacity: .55}},
    }],
  };
}

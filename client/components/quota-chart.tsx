import {createEffect, onCleanup, onMount} from 'solid-js';
import {init, use, type EChartsCoreOption} from 'echarts/core';
import {CustomChart, SankeyChart} from 'echarts/charts';
import {TooltipComponent, AriaComponent} from 'echarts/components';
import {LabelLayout} from 'echarts/features';
import {SVGRenderer} from 'echarts/renderers';
use([CustomChart, SankeyChart, TooltipComponent, AriaComponent, LabelLayout, SVGRenderer]);

export default function QuotaChart(props: {option: EChartsCoreOption; label: string; height: number; onScope?: (scope: string[]) => void; onWidth?: (width: number) => void}) {
  let element!: HTMLDivElement;
  onMount(() => {
    const chart = init(element, undefined, {renderer: 'svg'});
    createEffect(() => {
      chart.setOption({...props.option, aria: {enabled: true, label: {description: props.label}}, animation: false}, {notMerge: true});
    });
    chart.on('click', (event: unknown) => {
      const scope = (event as {data?: {scope?: string[]}}).data?.scope;
      if (scope) props.onScope?.(scope);
    });
    const observer = new ResizeObserver(() => { chart.resize(); props.onWidth?.(element.clientWidth); });
    observer.observe(element);
    onCleanup(() => {observer.disconnect(); chart.dispose();});
  });
  return <div class="quota-chart" ref={element} style={{height: `${props.height}px`}} role="img" aria-label={props.label}/>;
}

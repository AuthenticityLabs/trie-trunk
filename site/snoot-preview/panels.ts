import type {PanelDefinition} from '../../client/services/fast-dashboard';

// The marketing variant changes a supported metric, not the chart renderer.
export function previewPanels(panels:PanelDefinition[],mode?:string):PanelDefinition[]{
 return mode==='marketing'?panels.map((panel):PanelDefinition=>panel.id==='top'
  ?{...panel,title:'Top 10 Errors',data:{...panel.data,metric:'errors'}}
  :panel):panels;
}

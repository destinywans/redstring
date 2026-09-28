/** Registers every mock endpoint. Each domain file mirrors a section of docs/api/v3-contract.md. */
import "./domains/identity";
import "./domains/instance";
import "./domains/home";
import "./domains/conversations";
import "./domains/canvases";
import "./domains/canvas";
import "./domains/templates";
// Static create/draft paths (e.g. /incidents/draft) must register before the :id routes of their domain.
import "./domains/create";
import "./domains/events";
import "./domains/knowledge";
import "./domains/observe";
import "./domains/traces";
import "./domains/integrations";
import "./domains/agents";
import "./domains/settings";
import "./domains/composer";
